"""Embedding-based search module router (Layer 1).

Uses BGE embeddings to match incoming queries against precomputed module
scope vectors.  Returns the top-K most relevant modules **plus** all
baseline general-purpose engines, ensuring every query has broad coverage
regardless of routing quality.

This is the fast, local-only fallback layer -- no LLM calls required.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from diting.log import get_logger

if TYPE_CHECKING:
    from diting.modules.base import BaseSearchModule
    from diting.rerank.embedder import BGEEmbedder

logger = get_logger("routing.embedding")


class EmbeddingRouter:
    """Route queries to the most relevant search modules via embedding similarity.

    Parameters
    ----------
    modules:
        Enabled search module instances.  Modules without a ``MANIFEST``
        are logged as warnings and always included (treated as unclassified).
    embedder:
        A :class:`BGEEmbedder` instance for computing query / scope embeddings.
    top_k:
        Maximum number of *non-baseline* modules to select per query.
    """

    def __init__(
        self,
        modules: list[BaseSearchModule],
        embedder: BGEEmbedder,
        *,
        top_k: int = 5,
    ) -> None:
        self._embedder = embedder
        self._top_k = top_k

        # Partition modules into baseline (always-on) and routable.
        self._baseline_names: list[str] = []
        self._routable_names: list[str] = []
        self._routable_scopes: list[str] = []
        self._unclassified_names: list[str] = []

        for mod in modules:
            manifest = mod.manifest
            if manifest is None:
                logger.warning(
                    "Module %r has no manifest -- always included", mod.name
                )
                self._unclassified_names.append(mod.name)
                continue

            if manifest.result_type == "general":
                self._baseline_names.append(mod.name)
            else:
                self._routable_names.append(mod.name)
                self._routable_scopes.append(manifest.scope)

        # Precompute scope embeddings (lazy -- deferred to first route() call
        # to avoid blocking construction when the model is not yet loaded).
        self._scope_embeddings = None  # (N, D) numpy array or None

        logger.info(
            "EmbeddingRouter: baseline=%s, routable=%s, unclassified=%s, top_k=%d",
            self._baseline_names,
            self._routable_names,
            self._unclassified_names,
            self._top_k,
        )

    def _ensure_scope_embeddings(self) -> bool:
        """Compute and cache scope embeddings.  Returns True on success."""
        if self._scope_embeddings is not None:
            return True
        if not self._routable_scopes:
            return False
        try:
            self._scope_embeddings = self._embedder.embed(self._routable_scopes)
            return True
        except Exception:
            logger.warning(
                "Failed to embed module scopes -- falling back to all modules",
                exc_info=True,
            )
            return False

    def route(self, query: str) -> list[str]:
        """Select modules for *query*.

        Returns a list of module names.  Baseline general engines and
        unclassified modules are always included.  Up to ``top_k``
        non-baseline modules are selected by cosine similarity between
        the query embedding and precomputed scope embeddings.

        On any failure (embedding unavailable, numpy missing, etc.) the
        router degrades gracefully and returns **all** module names.
        """
        all_names = (
            self._baseline_names
            + self._routable_names
            + self._unclassified_names
        )

        # If there are no routable modules, just return everything.
        if not self._routable_names:
            return all_names

        # If top_k covers all routable modules, skip embedding entirely.
        if self._top_k >= len(self._routable_names):
            return all_names

        # Ensure scope embeddings are ready.
        if not self._ensure_scope_embeddings():
            return all_names

        # Embed query.
        try:
            import numpy as np

            query_vec = self._embedder.embed([query])  # (1, D)
        except Exception:
            logger.warning(
                "Failed to embed query -- falling back to all modules",
                exc_info=True,
            )
            return all_names

        # Cosine similarity (both vectors are L2-normalized).
        similarities = (self._scope_embeddings @ query_vec.T).flatten()  # (N,)

        # Pick top-K routable modules by similarity.
        top_indices = np.argsort(similarities)[::-1][: self._top_k]
        selected_routable = [self._routable_names[i] for i in top_indices]

        result = self._baseline_names + selected_routable + self._unclassified_names

        logger.debug(
            "Routing result: query=%r, selected_routable=%s, similarities=%s",
            query[:80],
            selected_routable,
            {
                self._routable_names[i]: f"{similarities[i]:.3f}"
                for i in top_indices
            },
        )
        return result
