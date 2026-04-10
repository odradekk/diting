"""Tests for diting.rerank.embedder — BGE embedding model."""

from __future__ import annotations

from unittest.mock import MagicMock, patch  # noqa: F401 - patch used in tests

import numpy as np
import pytest

from diting.rerank.embedder import BGEEmbedder, EmbedderUnavailableError


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_embedder(dim: int = 4) -> BGEEmbedder:
    """Build a BGEEmbedder with pre-injected mock tokenizer + session."""
    tokenizer = MagicMock()
    session = MagicMock()

    # Mock session.get_inputs() to return input names.
    inp_ids = MagicMock()
    inp_ids.name = "input_ids"
    inp_mask = MagicMock()
    inp_mask.name = "attention_mask"
    session.get_inputs.return_value = [inp_ids, inp_mask]

    embedder = BGEEmbedder(
        tokenizer=tokenizer,
        session=session,
    )
    # Manually set _input_names since _ensure_loaded won't run.
    embedder._input_names = frozenset(["input_ids", "attention_mask"])
    return embedder


def _setup_mock_outputs(
    embedder: BGEEmbedder,
    batch_size: int,
    seq_len: int = 5,
    dim: int = 4,
) -> None:
    """Configure tokenizer + session mocks to return realistic shapes."""
    tokenizer = embedder._tokenizer
    session = embedder._session

    tokenizer.return_value = {
        "input_ids": np.ones((batch_size, seq_len), dtype=np.int64),
        "attention_mask": np.ones((batch_size, seq_len), dtype=np.int64),
    }
    # ONNX output: (batch, seq_len, hidden_dim) token embeddings.
    token_emb = np.random.randn(batch_size, seq_len, dim).astype(np.float32)
    session.run.return_value = [token_emb]


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------


class TestEmbed:
    def test_empty_input_returns_empty_array(self):
        embedder = _make_embedder()
        result = embedder.embed([])
        assert result.shape == (0, 0)

    def test_basic_embed(self):
        embedder = _make_embedder(dim=4)
        _setup_mock_outputs(embedder, batch_size=2, dim=4)

        result = embedder.embed(["hello", "world"])

        assert result.shape == (2, 4)
        # L2 normalized: each row should have norm ~1.
        norms = np.linalg.norm(result, axis=1)
        np.testing.assert_allclose(norms, 1.0, atol=1e-5)

    def test_single_text(self):
        embedder = _make_embedder(dim=8)
        _setup_mock_outputs(embedder, batch_size=1, dim=8)

        result = embedder.embed(["single text"])
        assert result.shape == (1, 8)

    def test_batching(self):
        """Texts exceeding batch_size are split into multiple batches."""
        embedder = BGEEmbedder(
            batch_size=2,
            tokenizer=MagicMock(),
            session=MagicMock(),
        )
        embedder._input_names = frozenset(["input_ids", "attention_mask"])

        dim = 4
        call_count = 0

        def fake_tokenizer_call(texts, **kwargs):
            n = len(texts)
            return {
                "input_ids": np.ones((n, 5), dtype=np.int64),
                "attention_mask": np.ones((n, 5), dtype=np.int64),
            }

        def fake_run(_, inputs):
            nonlocal call_count
            call_count += 1
            n = inputs["input_ids"].shape[0]
            return [np.random.randn(n, 5, dim).astype(np.float32)]

        embedder._tokenizer.side_effect = fake_tokenizer_call
        embedder._session.run.side_effect = fake_run

        result = embedder.embed(["a", "b", "c", "d", "e"])
        assert result.shape == (5, dim)
        assert call_count == 3  # ceil(5/2) = 3 batches


class TestEnsureLoaded:
    def test_numpy_missing_raises(self):
        embedder = BGEEmbedder()
        with patch("diting.rerank.embedder.np", None):
            with pytest.raises(EmbedderUnavailableError, match="numpy"):
                embedder._ensure_loaded()

    def test_resolve_model_path_raises_when_missing(self, tmp_path):
        with pytest.raises(EmbedderUnavailableError, match="No ONNX model"):
            BGEEmbedder._resolve_model_path(tmp_path)


class TestResolveModelPath:
    def test_finds_onnx_subdir(self, tmp_path):
        onnx_dir = tmp_path / "onnx"
        onnx_dir.mkdir()
        model_file = onnx_dir / "model.onnx"
        model_file.write_bytes(b"fake")

        result = BGEEmbedder._resolve_model_path(tmp_path)
        assert result == model_file

    def test_finds_root_model(self, tmp_path):
        model_file = tmp_path / "model.onnx"
        model_file.write_bytes(b"fake")

        result = BGEEmbedder._resolve_model_path(tmp_path)
        assert result == model_file

    def test_missing_raises(self, tmp_path):
        with pytest.raises(EmbedderUnavailableError, match="No ONNX model"):
            BGEEmbedder._resolve_model_path(tmp_path)


class TestErrorPaths:
    def test_embed_batch_no_ort_inputs_raises(self):
        embedder = _make_embedder()
        embedder._input_names = frozenset(["nonexistent_input"])
        # Tokenizer returns a dict — calling a MagicMock with kwargs still
        # returns its return_value, so set it explicitly.
        embedder._tokenizer.return_value = {
            "input_ids": np.ones((1, 5), dtype=np.int64),
        }
        with pytest.raises(EmbedderUnavailableError, match="no inputs"):
            embedder._embed_batch(["test"])

    def test_empty_outputs_raises(self):
        embedder = _make_embedder()
        # Set up tokenizer to return a valid dict.
        embedder._tokenizer.return_value = {
            "input_ids": np.ones((1, 5), dtype=np.int64),
            "attention_mask": np.ones((1, 5), dtype=np.int64),
        }
        embedder._session.run.return_value = []
        with pytest.raises(EmbedderUnavailableError, match="no outputs"):
            embedder._embed_batch(["test"])
