"""Tests for diting.fetch.content_validator."""

from diting.fetch.content_validator import is_cacheable


def _real_article(length: int = 800) -> str:
    body = (
        "Kubernetes is an open source container orchestration system for "
        "automating software deployment, scaling, and management. "
    )
    return body * (length // len(body) + 1)


def test_accepts_long_article() -> None:
    ok, _ = is_cacheable(_real_article())
    assert ok is True


def test_rejects_empty_string() -> None:
    ok, reason = is_cacheable("")
    assert ok is False
    assert reason == "empty"


def test_rejects_whitespace_only() -> None:
    ok, reason = is_cacheable("   \n  \t  ")
    assert ok is False
    assert reason == "empty"


def test_rejects_too_short() -> None:
    ok, reason = is_cacheable("short page")
    assert ok is False
    assert reason.startswith("too_short")


def test_rejects_cloudflare_challenge() -> None:
    content = "x" * 500 + "Checking your browser before accessing" + "y" * 500
    ok, reason = is_cacheable(content)
    assert ok is False
    assert reason.startswith("bot_wall")


def test_rejects_just_a_moment() -> None:
    content = "x" * 500 + "Just a moment..." + "y" * 500
    ok, reason = is_cacheable(content)
    assert ok is False
    assert reason.startswith("bot_wall")


def test_rejects_cloudflare_attention_page() -> None:
    content = "Attention Required! | Cloudflare" + "y" * 500
    ok, reason = is_cacheable(content)
    assert ok is False
    assert "bot_wall" in reason


def test_rejects_recaptcha_wall() -> None:
    content = "y" * 500 + 'class="g-recaptcha"' + "y" * 500
    ok, reason = is_cacheable(content)
    assert ok is False
    assert reason.startswith("bot_wall")


def test_rejects_x_login_wall() -> None:
    content = "y" * 500 + 'href="/i/flow/login"' + "y" * 500
    ok, reason = is_cacheable(content)
    assert ok is False
    assert reason.startswith("login_wall")


def test_rejects_zhihu_login_wall() -> None:
    content = "内容预览..." * 100 + "登录后查看完整内容"
    ok, reason = is_cacheable(content)
    assert ok is False
    assert reason.startswith("login_wall")


def test_rejects_rate_limit_page() -> None:
    content = "y" * 500 + "Too Many Requests" + "y" * 500
    ok, reason = is_cacheable(content)
    assert ok is False
    assert reason.startswith("rate_limit")


def test_accepts_article_mentioning_cloudflare() -> None:
    """A long technical article mentioning Cloudflare once should be cached.

    Legitimate content often references these terms; the guard is specific
    enough that casual mentions outside the trigger phrases are accepted.
    """
    content = (
        _real_article() + " We use Cloudflare to protect our origin, "
        "configured with aggressive caching rules and sensible TTLs. "
    ) * 3
    ok, reason = is_cacheable(content)
    assert ok is True
    assert reason == "ok"
