"""Local BGE reranker using ONNX Runtime.

This module is intentionally lazy: optional dependencies are imported only when
an actual rerank call occurs. If the runtime stack or model files are missing,
callers receive a typed error and can degrade to an LLM-based scorer.
"""

from __future__ import annotations

import os
import pathlib
from typing import Any

try:
    import numpy as np
except ImportError:  # pragma: no cover - optional dependency
    np = None  # type: ignore[assignment]

DEFAULT_BGE_MODEL = "BAAI/bge-reranker-base"
DEFAULT_BGE_MODEL_DIR = pathlib.Path.home() / ".cache" / "diting" / "models" / "bge-reranker-base"
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


class RerankerError(Exception):
    """Base reranker failure."""


class RerankerUnavailableError(RerankerError):
    """Raised when the local reranker stack or model is unavailable."""


class BGEReranker:
    """Run BGE reranking locally via ONNX Runtime.

    Parameters
    ----------
    model_id:
        Hugging Face model id. Defaults to ``BAAI/bge-reranker-base``.
    model_dir:
        Local cache directory for tokenizer + ONNX files.
    max_length:
        Max tokenizer sequence length per query/document pair.
    batch_size:
        Number of pairs per inference batch.
    providers:
        Explicit ONNX Runtime providers. Defaults to CPU.
    """

    def __init__(
        self,
        model_id: str = DEFAULT_BGE_MODEL,
        *,
        model_dir: str | pathlib.Path = DEFAULT_BGE_MODEL_DIR,
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

    def rerank(self, query: str, docs: list[str]) -> list[float]:
        """Return sigmoid-normalized relevance scores for *docs*."""
        if not docs:
            return []

        self._ensure_loaded()
        scores: list[float] = []
        for start in range(0, len(docs), self._batch_size):
            batch_docs = docs[start : start + self._batch_size]
            scores.extend(self._score_batch(query, batch_docs))
        return scores

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    def _ensure_loaded(self) -> None:
        if self._tokenizer is not None and self._session is not None:
            return

        if np is None:
            raise RerankerUnavailableError(
                "Local reranker requires numpy; install with `pip install diting[rerank]`."
            )

        try:
            import onnxruntime as ort  # type: ignore
            from huggingface_hub import snapshot_download  # type: ignore
            from transformers import AutoTokenizer  # type: ignore
        except ImportError as exc:  # pragma: no cover - depends on optional deps
            raise RerankerUnavailableError(
                "Local reranker requires optional dependencies; install with `pip install diting[rerank]`."
            ) from exc

        self._model_dir.mkdir(parents=True, exist_ok=True)
        try:
            snapshot_download(
                repo_id=self._model_id,
                local_dir=str(self._model_dir),
                allow_patterns=_DEFAULT_ALLOW_PATTERNS,
            )
        except Exception as exc:  # pragma: no cover - network / hub failure
            raise RerankerUnavailableError(
                f"Failed to download reranker model {self._model_id}: {exc}"
            ) from exc

        try:
            tokenizer = AutoTokenizer.from_pretrained(str(self._model_dir))
        except Exception as exc:  # pragma: no cover - depends on downloaded model
            raise RerankerUnavailableError(
                f"Failed to load reranker tokenizer from {self._model_dir}: {exc}"
            ) from exc

        model_path = self._resolve_model_path(self._model_dir)
        session_options = ort.SessionOptions()
        cpu_count = os.cpu_count() or 1
        session_options.intra_op_num_threads = min(8, max(1, cpu_count))
        session_options.inter_op_num_threads = min(4, max(1, cpu_count))
        session_options.graph_optimization_level = ort.GraphOptimizationLevel.ORT_ENABLE_ALL
        session_options.execution_mode = ort.ExecutionMode.ORT_PARALLEL

        try:
            session = ort.InferenceSession(
                str(model_path),
                sess_options=session_options,
                providers=self._providers,
            )
        except Exception as exc:  # pragma: no cover - depends on runtime stack
            raise RerankerUnavailableError(
                f"Failed to load ONNX reranker model from {model_path}: {exc}"
            ) from exc

        self._tokenizer = tokenizer
        self._session = session
        self._input_names = frozenset(inp.name for inp in session.get_inputs())

    def _score_batch(self, query: str, docs: list[str]) -> list[float]:
        assert self._tokenizer is not None
        assert self._session is not None
        assert self._input_names is not None

        encoded = self._tokenizer(
            [query] * len(docs),
            docs,
            padding=True,
            truncation="only_second",
            max_length=self._max_length,
            return_tensors="np",
        )

        ort_inputs = {
            name: value
            for name, value in encoded.items()
            if name in self._input_names
        }
        if not ort_inputs:
            raise RerankerUnavailableError(
                f"Tokenizer did not produce inputs expected by ONNX session: {sorted(self._input_names)}"
            )

        outputs = self._session.run(None, ort_inputs)
        if not outputs:
            raise RerankerUnavailableError("ONNX reranker returned no outputs")

        logits = outputs[0].reshape(-1).astype(np.float64)
        scores = np.where(
            logits >= 0,
            1.0 / (1.0 + np.exp(-logits)),
            np.exp(logits) / (1.0 + np.exp(logits)),
        )
        return scores.tolist()

    @staticmethod
    def _resolve_model_path(model_dir: pathlib.Path) -> pathlib.Path:
        candidates = [
            model_dir / "onnx" / "model.onnx",
            model_dir / "model.onnx",
        ]
        for candidate in candidates:
            if candidate.is_file():
                return candidate
        raise RerankerUnavailableError(
            f"No ONNX model found under {model_dir}; expected one of: {', '.join(str(p) for p in candidates)}"
        )


