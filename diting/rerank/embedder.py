"""Local BGE embedding model using ONNX Runtime.

Same lazy-loading strategy as :mod:`diting.rerank.bge`: optional deps are
imported only on first use; callers receive :class:`EmbedderUnavailableError`
and can skip semantic dedup gracefully.
"""

from __future__ import annotations

import os
import pathlib
from typing import Any

try:
    import numpy as np
except ImportError:  # pragma: no cover - optional dependency
    np = None  # type: ignore[assignment]

DEFAULT_EMBED_MODEL = "BAAI/bge-small-en-v1.5"
DEFAULT_EMBED_MODEL_DIR = (
    pathlib.Path.home() / ".cache" / "diting" / "models" / "bge-small-en-v1.5"
)
_DEFAULT_ALLOW_PATTERNS = [
    "config.json",
    "tokenizer.json",
    "tokenizer_config.json",
    "special_tokens_map.json",
    "sentencepiece.bpe.model",
    "sentencepiece.model",
    "spiece.model",
    "vocab.json",
    "merges.txt",
    "onnx/*",
]


class EmbedderError(Exception):
    """Base embedder failure."""


class EmbedderUnavailableError(EmbedderError):
    """Raised when the embedding model or runtime stack is unavailable."""


class BGEEmbedder:
    """Produce L2-normalized embeddings via a BGE ONNX model.

    Parameters
    ----------
    model_id:
        Hugging Face model id.  Defaults to ``BAAI/bge-small-en-v1.5``.
    model_dir:
        Local cache directory for tokenizer + ONNX files.
    max_length:
        Max tokenizer sequence length.
    batch_size:
        Number of texts per inference batch.
    providers:
        Explicit ONNX Runtime providers.  Defaults to CPU.
    """

    def __init__(
        self,
        model_id: str = DEFAULT_EMBED_MODEL,
        *,
        model_dir: str | pathlib.Path = DEFAULT_EMBED_MODEL_DIR,
        max_length: int = 512,
        batch_size: int = 32,
        providers: list[str] | None = None,
        tokenizer: Any | None = None,
        session: Any | None = None,
    ) -> None:
        self._model_id = model_id
        self._model_dir = pathlib.Path(model_dir)
        self._max_length = max_length
        self._batch_size = batch_size
        self._providers = providers or ["CPUExecutionProvider"]
        self._tokenizer = tokenizer
        self._session = session
        self._input_names: frozenset[str] | None = None

    @property
    def model_id(self) -> str:
        return self._model_id

    @property
    def model_dir(self) -> pathlib.Path:
        return self._model_dir

    def embed(self, texts: list[str]) -> Any:
        """Return L2-normalized embeddings as an ``(N, D)`` numpy array."""
        if np is None:
            raise EmbedderUnavailableError(
                "Embedder requires numpy; install with `pip install diting[rerank]`."
            )
        if not texts:
            return np.empty((0, 0), dtype=np.float32)

        self._ensure_loaded()
        parts: list[Any] = []
        for start in range(0, len(texts), self._batch_size):
            batch = texts[start : start + self._batch_size]
            parts.append(self._embed_batch(batch))
        return np.vstack(parts)

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    def _ensure_loaded(self) -> None:
        if self._tokenizer is not None and self._session is not None:
            return

        if np is None:
            raise EmbedderUnavailableError(
                "Embedder requires numpy; install with `pip install diting[rerank]`."
            )

        try:
            import onnxruntime as ort  # type: ignore
            from huggingface_hub import snapshot_download  # type: ignore
            from transformers import AutoTokenizer  # type: ignore
        except ImportError as exc:  # pragma: no cover - depends on optional deps
            raise EmbedderUnavailableError(
                "Embedder requires optional dependencies; "
                "install with `pip install diting[rerank]`."
            ) from exc

        self._model_dir.mkdir(parents=True, exist_ok=True)
        try:
            snapshot_download(
                repo_id=self._model_id,
                local_dir=str(self._model_dir),
                allow_patterns=_DEFAULT_ALLOW_PATTERNS,
            )
        except Exception as exc:  # pragma: no cover - network / hub failure
            raise EmbedderUnavailableError(
                f"Failed to download embedding model {self._model_id}: {exc}"
            ) from exc

        try:
            tokenizer = AutoTokenizer.from_pretrained(str(self._model_dir))
        except Exception as exc:  # pragma: no cover - depends on downloaded model
            raise EmbedderUnavailableError(
                f"Failed to load tokenizer from {self._model_dir}: {exc}"
            ) from exc

        model_path = self._resolve_model_path(self._model_dir)
        session_options = ort.SessionOptions()
        cpu_count = os.cpu_count() or 1
        session_options.intra_op_num_threads = min(8, max(1, cpu_count))
        session_options.inter_op_num_threads = min(4, max(1, cpu_count))
        session_options.graph_optimization_level = (
            ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        )
        session_options.execution_mode = ort.ExecutionMode.ORT_PARALLEL

        try:
            session = ort.InferenceSession(
                str(model_path),
                sess_options=session_options,
                providers=self._providers,
            )
        except Exception as exc:  # pragma: no cover - depends on runtime stack
            raise EmbedderUnavailableError(
                f"Failed to load ONNX embedding model from {model_path}: {exc}"
            ) from exc

        self._tokenizer = tokenizer
        self._session = session
        self._input_names = frozenset(inp.name for inp in session.get_inputs())

    def _embed_batch(self, texts: list[str]) -> Any:
        assert self._tokenizer is not None
        assert self._session is not None
        assert self._input_names is not None

        encoded = self._tokenizer(
            texts,
            padding=True,
            truncation=True,
            max_length=self._max_length,
            return_tensors="np",
        )

        ort_inputs = {
            name: value
            for name, value in encoded.items()
            if name in self._input_names
        }
        if not ort_inputs:
            raise EmbedderUnavailableError(
                f"Tokenizer produced no inputs expected by ONNX session: "
                f"{sorted(self._input_names)}"
            )

        outputs = self._session.run(None, ort_inputs)
        if not outputs:
            raise EmbedderUnavailableError("ONNX embedding model returned no outputs")

        # Mean pooling over token embeddings, masked by attention_mask.
        token_embeddings = outputs[0]  # (batch, seq_len, hidden_dim)
        attention_mask = encoded["attention_mask"]  # (batch, seq_len)

        mask_expanded = np.expand_dims(attention_mask, axis=-1)  # (batch, seq, 1)
        summed = np.sum(token_embeddings * mask_expanded, axis=1)  # (batch, dim)
        counts = np.clip(np.sum(mask_expanded, axis=1), a_min=1e-9, a_max=None)
        mean_pooled = summed / counts  # (batch, dim)

        # L2 normalize.
        norms = np.linalg.norm(mean_pooled, axis=1, keepdims=True)
        norms = np.clip(norms, a_min=1e-12, a_max=None)
        return (mean_pooled / norms).astype(np.float32)

    @staticmethod
    def _resolve_model_path(model_dir: pathlib.Path) -> pathlib.Path:
        candidates = [
            model_dir / "onnx" / "model.onnx",
            model_dir / "model.onnx",
        ]
        for candidate in candidates:
            if candidate.is_file():
                return candidate
        raise EmbedderUnavailableError(
            f"No ONNX model found under {model_dir}; "
            f"expected one of: {', '.join(str(p) for p in candidates)}"
        )
