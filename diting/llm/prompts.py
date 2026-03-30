"""Prompt loading system with configurable search paths."""

from __future__ import annotations

import pathlib
from typing import ClassVar


class PromptLoader:
    """Load prompt templates from a prioritized chain of directories.

    Resolution order (highest to lowest priority):
    1. ``prompts_dir`` constructor argument (typically from PROMPTS_DIR env var)
    2. ``.diting/prompts/`` in the current working directory
    3. ``~/.diting/prompts/`` in the user's home directory
    4. Built-in defaults from ``prompts/`` in the project root
    """

    VALID_NAMES: ClassVar[set[str]] = {
        "classification",
        "query_generation",
        "scoring",
        "quality_evaluation",
        "summarization",
    }

    def __init__(
        self,
        prompts_dir: str = "",
        project_root: str | None = None,
    ) -> None:
        """Initialise the loader.

        Parameters
        ----------
        prompts_dir:
            Explicit directory to search first (e.g. from ``PROMPTS_DIR`` env
            var).  Empty string means "not set".
        project_root:
            Project root used to locate built-in defaults under ``prompts/``.
            When *None*, auto-detected by walking up from this source file.
        """
        self._search_dirs = self._build_search_dirs(prompts_dir, project_root)

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    def load(self, name: str) -> str:
        """Load a prompt by *name*.

        Returns the prompt content as a string.

        Raises
        ------
        ValueError
            If *name* is not one of :pyattr:`VALID_NAMES`.
        FileNotFoundError
            If no file could be found for *name* in any search directory.
        """
        if name not in self.VALID_NAMES:
            raise ValueError(
                f"Invalid prompt name {name!r}. "
                f"Must be one of: {', '.join(sorted(self.VALID_NAMES))}"
            )

        filename = f"{name}.md"
        for directory in self._search_dirs:
            candidate = directory / filename
            if candidate.is_file():
                return candidate.read_text(encoding="utf-8")

        searched = ", ".join(str(d) for d in self._search_dirs)
        raise FileNotFoundError(
            f"Prompt {name!r} not found in any search directory: {searched}"
        )

    # ------------------------------------------------------------------
    # Internals
    # ------------------------------------------------------------------

    def _build_search_dirs(
        self,
        prompts_dir: str,
        project_root: str | None,
    ) -> list[pathlib.Path]:
        """Assemble the ordered list of directories to search."""
        dirs: list[pathlib.Path] = []

        # 1. Explicit prompts_dir
        if prompts_dir:
            path = pathlib.Path(prompts_dir)
            if path.is_dir():
                dirs.append(path)

        # 2. .diting/prompts/ in cwd
        local = pathlib.Path.cwd() / ".diting" / "prompts"
        if local.is_dir():
            dirs.append(local)

        # 3. ~/.diting/prompts/ in home
        home = pathlib.Path.home() / ".diting" / "prompts"
        if home.is_dir():
            dirs.append(home)

        # 4. Built-in defaults
        if project_root is not None:
            builtin = pathlib.Path(project_root) / "prompts"
        else:
            builtin = self._detect_project_root() / "prompts"
        if builtin.is_dir():
            dirs.append(builtin)

        return dirs

    @staticmethod
    def _detect_project_root() -> pathlib.Path:
        """Walk up from this file to find the project root.

        The project root is identified by the presence of ``pyproject.toml``.
        Falls back to the grandparent of the ``src/`` package directory.
        """
        current = pathlib.Path(__file__).resolve().parent
        for parent in (current, *current.parents):
            if (parent / "pyproject.toml").is_file():
                return parent
        # Fallback: src/diting/llm -> three levels up is project root
        return pathlib.Path(__file__).resolve().parent.parent.parent.parent
