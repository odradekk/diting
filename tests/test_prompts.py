"""Tests for prompt loading system."""

from __future__ import annotations

import pathlib

import pytest

from diting.llm.prompts import PromptLoader


@pytest.fixture()
def project_root() -> pathlib.Path:
    """Real project root for testing built-in defaults."""
    return pathlib.Path(__file__).resolve().parent.parent


class TestLoadBuiltinDefault:
    """When no custom dirs exist, loads from project prompts/ dir."""

    def test_loads_query_generation(self, project_root: pathlib.Path) -> None:
        loader = PromptLoader(prompts_dir="", project_root=str(project_root))
        content = loader.load("query_generation")
        assert "search query generator" in content.lower()

    def test_loads_scoring(self, project_root: pathlib.Path) -> None:
        loader = PromptLoader(prompts_dir="", project_root=str(project_root))
        content = loader.load("scoring")
        assert "search result scorer" in content.lower()


class TestLoadFromPromptsDir:
    """When prompts_dir is set and has the file, loads from there."""

    def test_loads_from_custom_dir(self, tmp_path: pathlib.Path) -> None:
        prompts_dir = tmp_path / "custom_prompts"
        prompts_dir.mkdir()
        (prompts_dir / "query_generation.md").write_text("custom prompt content")

        loader = PromptLoader(prompts_dir=str(prompts_dir))
        content = loader.load("query_generation")
        assert content == "custom prompt content"

    def test_ignores_nonexistent_dir(
        self, tmp_path: pathlib.Path, project_root: pathlib.Path
    ) -> None:
        loader = PromptLoader(
            prompts_dir=str(tmp_path / "does_not_exist"),
            project_root=str(project_root),
        )
        # Falls back to builtin
        content = loader.load("query_generation")
        assert "search query generator" in content.lower()


class TestLoadFromLocalDiting:
    """When .diting/prompts/ exists in cwd, loads from there."""

    def test_loads_from_local_dir(
        self, tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        local_prompts = tmp_path / ".diting" / "prompts"
        local_prompts.mkdir(parents=True)
        (local_prompts / "scoring.md").write_text("local scoring prompt")

        monkeypatch.chdir(tmp_path)

        loader = PromptLoader(prompts_dir="")
        content = loader.load("scoring")
        assert content == "local scoring prompt"


class TestLoadFromHomeDiting:
    """When ~/.diting/prompts/ exists, loads from there."""

    def test_loads_from_home_dir(
        self, tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        fake_home = tmp_path / "fake_home"
        fake_home.mkdir()
        home_prompts = fake_home / ".diting" / "prompts"
        home_prompts.mkdir(parents=True)
        (home_prompts / "summarization.md").write_text("home summarization prompt")

        monkeypatch.setenv("HOME", str(fake_home))

        loader = PromptLoader(prompts_dir="")
        content = loader.load("summarization")
        assert content == "home summarization prompt"


class TestPriorityOrder:
    """prompts_dir > .diting/prompts/ (cwd) > ~/.diting/prompts/ > builtin."""

    def test_prompts_dir_beats_local(
        self, tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        # Set up prompts_dir
        custom = tmp_path / "custom"
        custom.mkdir()
        (custom / "scoring.md").write_text("from prompts_dir")

        # Set up local .diting/prompts/
        cwd = tmp_path / "workdir"
        cwd.mkdir()
        local_prompts = cwd / ".diting" / "prompts"
        local_prompts.mkdir(parents=True)
        (local_prompts / "scoring.md").write_text("from local")

        monkeypatch.chdir(cwd)

        loader = PromptLoader(prompts_dir=str(custom))
        assert loader.load("scoring") == "from prompts_dir"

    def test_local_beats_home(
        self, tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        # Set up local .diting/prompts/
        cwd = tmp_path / "workdir"
        cwd.mkdir()
        local_prompts = cwd / ".diting" / "prompts"
        local_prompts.mkdir(parents=True)
        (local_prompts / "scoring.md").write_text("from local")

        # Set up home .diting/prompts/
        fake_home = tmp_path / "fake_home"
        fake_home.mkdir()
        home_prompts = fake_home / ".diting" / "prompts"
        home_prompts.mkdir(parents=True)
        (home_prompts / "scoring.md").write_text("from home")

        monkeypatch.chdir(cwd)
        monkeypatch.setenv("HOME", str(fake_home))

        loader = PromptLoader(prompts_dir="")
        assert loader.load("scoring") == "from local"

    def test_home_beats_builtin(
        self, tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch,
        project_root: pathlib.Path,
    ) -> None:
        # Set up home .diting/prompts/
        fake_home = tmp_path / "fake_home"
        fake_home.mkdir()
        home_prompts = fake_home / ".diting" / "prompts"
        home_prompts.mkdir(parents=True)
        (home_prompts / "scoring.md").write_text("from home")

        # cwd has no .diting
        monkeypatch.chdir(tmp_path)
        monkeypatch.setenv("HOME", str(fake_home))

        loader = PromptLoader(prompts_dir="", project_root=str(project_root))
        assert loader.load("scoring") == "from home"

    def test_full_priority_chain(
        self, tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch,
        project_root: pathlib.Path,
    ) -> None:
        """All four sources present: prompts_dir wins."""
        # prompts_dir
        custom = tmp_path / "custom"
        custom.mkdir()
        (custom / "scoring.md").write_text("from prompts_dir")

        # local
        cwd = tmp_path / "workdir"
        cwd.mkdir()
        local_prompts = cwd / ".diting" / "prompts"
        local_prompts.mkdir(parents=True)
        (local_prompts / "scoring.md").write_text("from local")

        # home
        fake_home = tmp_path / "fake_home"
        fake_home.mkdir()
        home_prompts = fake_home / ".diting" / "prompts"
        home_prompts.mkdir(parents=True)
        (home_prompts / "scoring.md").write_text("from home")

        monkeypatch.chdir(cwd)
        monkeypatch.setenv("HOME", str(fake_home))

        loader = PromptLoader(
            prompts_dir=str(custom), project_root=str(project_root)
        )
        assert loader.load("scoring") == "from prompts_dir"


class TestFallbackChain:
    """If higher-priority path doesn't have the file, falls back to next."""

    def test_prompts_dir_missing_file_falls_to_local(
        self, tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        # prompts_dir exists but has no scoring.md
        custom = tmp_path / "custom"
        custom.mkdir()
        (custom / "query_generation.md").write_text("has this one")

        # local has scoring.md
        cwd = tmp_path / "workdir"
        cwd.mkdir()
        local_prompts = cwd / ".diting" / "prompts"
        local_prompts.mkdir(parents=True)
        (local_prompts / "scoring.md").write_text("from local fallback")

        monkeypatch.chdir(cwd)

        loader = PromptLoader(prompts_dir=str(custom))
        assert loader.load("scoring") == "from local fallback"

    def test_local_missing_file_falls_to_home(
        self, tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        # local exists but has no scoring.md
        cwd = tmp_path / "workdir"
        cwd.mkdir()
        local_prompts = cwd / ".diting" / "prompts"
        local_prompts.mkdir(parents=True)
        (local_prompts / "query_generation.md").write_text("has this one")

        # home has scoring.md
        fake_home = tmp_path / "fake_home"
        fake_home.mkdir()
        home_prompts = fake_home / ".diting" / "prompts"
        home_prompts.mkdir(parents=True)
        (home_prompts / "scoring.md").write_text("from home fallback")

        monkeypatch.chdir(cwd)
        monkeypatch.setenv("HOME", str(fake_home))

        loader = PromptLoader(prompts_dir="")
        assert loader.load("scoring") == "from home fallback"

    def test_all_custom_missing_falls_to_builtin(
        self, tmp_path: pathlib.Path, monkeypatch: pytest.MonkeyPatch,
        project_root: pathlib.Path,
    ) -> None:
        # prompts_dir exists but empty
        custom = tmp_path / "custom"
        custom.mkdir()

        # local exists but empty
        cwd = tmp_path / "workdir"
        cwd.mkdir()
        local_prompts = cwd / ".diting" / "prompts"
        local_prompts.mkdir(parents=True)

        # home exists but empty
        fake_home = tmp_path / "fake_home"
        fake_home.mkdir()
        home_prompts = fake_home / ".diting" / "prompts"
        home_prompts.mkdir(parents=True)

        monkeypatch.chdir(cwd)
        monkeypatch.setenv("HOME", str(fake_home))

        loader = PromptLoader(
            prompts_dir=str(custom), project_root=str(project_root)
        )
        content = loader.load("query_generation")
        assert "search query generator" in content.lower()


class TestInvalidName:
    """ValueError for unknown prompt names."""

    def test_raises_for_unknown_name(self, project_root: pathlib.Path) -> None:
        loader = PromptLoader(prompts_dir="", project_root=str(project_root))
        with pytest.raises(ValueError, match="Invalid prompt name"):
            loader.load("nonexistent")

    def test_raises_for_empty_name(self, project_root: pathlib.Path) -> None:
        loader = PromptLoader(prompts_dir="", project_root=str(project_root))
        with pytest.raises(ValueError, match="Invalid prompt name"):
            loader.load("")


class TestValidNames:
    """All five names load successfully from defaults."""

    @pytest.mark.parametrize(
        "name",
        ["query_generation", "scoring", "quality_evaluation", "summarization"],
    )
    def test_all_valid_names_load(
        self, name: str, project_root: pathlib.Path
    ) -> None:
        loader = PromptLoader(prompts_dir="", project_root=str(project_root))
        content = loader.load(name)
        assert isinstance(content, str)
        assert len(content) > 0

    def test_valid_names_class_attribute(self) -> None:
        assert PromptLoader.VALID_NAMES == {
            "query_generation",
            "scoring",
            "quality_evaluation",
            "summarization",
        }
