# Fetch Layer Benchmark Report

**Date**: 2026-04-12 17:09

## Summary

**Total**: 50 URLs | **Success**: 50 (100.0%) | **Failed**: 0 (0.0%)

## Success Rate by Difficulty

| Difficulty | Total | Success | Rate | Avg Latency |
|---|---|---|---|---|
| easy | 19 | 19 | 100.0% | 5716ms |
| medium | 20 | 20 | 100.0% | 4805ms |
| hard | 11 | 11 | 100.0% | 3355ms |

## Success Rate by Content Type

| Content Type | Total | Success | Rate |
|---|---|---|---|
| academic | 5 | 5 | 100.0% |
| article | 10 | 10 | 100.0% |
| code | 4 | 4 | 100.0% |
| documentation | 10 | 10 | 100.0% |
| forum | 8 | 8 | 100.0% |
| landing | 5 | 5 | 100.0% |
| mixed | 3 | 3 | 100.0% |
| wiki | 5 | 5 | 100.0% |

## Layer Distribution (successful fetches)

| Layer | Count | % of Success |
|---|---|---|
| utls | 28 | 56.0% |
| chromedp | 6 | 12.0% |
| jina | 16 | 32.0% |

## Content Quality (successful fetches)

| Metric | Value |
|---|---|
| Title extracted | 40 / 50 (80%) |
| Encoding OK (valid UTF-8) | 49 / 50 (98%) |
| Zero noise indicators | 38 / 50 (76%) |
| No code-sample HTML | 30 / 50 (60%) |
| Avg content length | 16805 chars |
| Avg paragraphs | 42 |

## Per-URL Results

| # | Diff | Type | URL | OK | Layer | ms | Chars | Title | Noise | Code |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | easy | academic | https://arxiv.org/abs/1706.03762 | ✅ | jina | 4195 | 10763 | [1706.03762] Attention... | 1 | 1 |
| 2 | easy | academic | https://aclanthology.org/D19-1410/ | ✅ | utls | 852 | 1128 | Sentence-BERT: Sentenc... | 0 | 0 |
| 3 | easy | academic | https://www.jmlr.org/papers/v3/blei03a.html | ✅ | utls | 399 | 968 |  | 0 | 0 |
| 4 | medium | academic | https://catalog.he.u-tokyo.ac.jp/detail?code=05... | ✅ | utls | 543 | 2730 | 統計的機械学習 ... | 0 | 0 |
| 5 | easy | academic | https://www.stochastik.uni-freiburg.de/de/lehre... | ✅ | jina | 7986 | 3606 |  | 0 | 0 |
| 6 | easy | article | https://martinfowler.com/articles/microservices... | ✅ | utls | 1003 | 32013 | Microservices | 0 | 0 |
| 7 | easy | article | https://overreacted.io/a-complete-guide-to-usee... | ✅ | utls | 628 | 32017 | A Complete Guide to us... | 1 | 33 |
| 8 | easy | article | https://www.joelonsoftware.com/2001/12/11/back-... | ✅ | utls | 423 | 18063 | Back to Basics | 0 | 7 |
| 9 | easy | article | https://paulgraham.com/startupideas.html | ✅ | utls | 1138 | 32017 | How to Get Startup Ideas | 0 | 0 |
| 10 | medium | article | https://stripe.com/blog/idempotency | ✅ | utls | 1025 | 8450 | Designing robust and p... | 0 | 0 |
| 11 | medium | article | https://aws.amazon.com/blogs/architecture/expon... | ✅ | utls | 670 | 6224 | Exponential Backoff An... | 0 | 0 |
| 12 | medium | article | https://github.blog/engineering/infrastructure/... | ✅ | jina | 5601 | 32014 |  | 0 | 2 |
| 13 | medium | article | https://blog.cloudflare.com/sometimes-i-cache/ | ✅ | utls | 9183 | 17816 | Sometimes I cache: imp... | 0 | 0 |
| 14 | hard | article | https://www.notion.com/blog/how-we-sped-up-noti... | ✅ | utls | 6270 | 13108 | Notion engineers sped ... | 0 | 0 |
| 15 | hard | article | https://vercel.com/blog/ai-sdk-5 | ✅ | jina | 6786 | 32019 |  | 0 | 21 |
| 16 | medium | code | https://github.com/scalar/scalar/blob/main/READ... | ✅ | utls | 1216 | 30968 | scalar/README.md at ma... | 0 | 9 |
| 17 | medium | code | https://gist.github.com/gaearon/ffd88b0e4f00b22... | ✅ | utls | 1709 | 6732 | Redux without the sani... | 0 | 0 |
| 18 | medium | code | https://gitlab.com/gitlab-org/gitlab-foss/blob/... | ✅ | jina | 8342 | 11855 |  | 1 | 0 |
| 19 | hard | code | https://bitbucket.org/tildeslash/monit/src/master/ | ✅ | jina | 5121 | 5939 |  | 2 | 1 |
| 20 | easy | documentation | https://go.dev/doc/effective_go | ✅ | utls | 757 | 32014 | Effective Go - The Go ... | 0 | 1 |
| 21 | easy | documentation | https://docs.python.org/3/tutorial/controlflow.... | ✅ | utls | 424 | 32018 | 4. More Control Flow T... | 0 | 13 |
| 22 | easy | documentation | https://www.postgresql.org/docs/current/sql-sel... | ✅ | utls | 1117 | 32015 | SELECT | 0 | 0 |
| 23 | medium | documentation | https://developer.mozilla.org/en-US/docs/Web/HT... | ✅ | jina | 5142 | 31983 | 429 Too Many Requests ... | 1 | 6 |
| 24 | medium | documentation | https://react.dev/reference/react/useEffect | ✅ | jina | 4281 | 32019 | useEffect – React | 0 | 58 |
| 25 | medium | documentation | https://docs.github.com/en/repositories/creatin... | ✅ | utls | 377 | 4217 | About repositories - G... | 0 | 0 |
| 26 | medium | documentation | https://docs.stripe.com/webhooks | ✅ | utls | 1388 | 26264 | Receive Stripe events ... | 2 | 4 |
| 27 | hard | documentation | https://developers.cloudflare.com/workers/runti... | ✅ | utls | 454 | 4172 | Fetch · Cloudflare Wo... | 0 | 2 |
| 28 | hard | documentation | https://nextjs.org/docs/app/getting-started/ins... | ✅ | utls | 513 | 5634 | Getting Started: Insta... | 0 | 2 |
| 29 | hard | documentation | https://www.mongodb.com/docs/manual/tutorial/qu... | ✅ | utls | 1499 | 32018 | Query Documents - Data... | 0 | 83 |
| 30 | medium | forum | https://stackoverflow.com/questions/11227809/wh... | ✅ | jina | 30934 | 32015 |  | 1 | 4 |
| 31 | medium | forum | https://askubuntu.com/questions/318315/how-can-... | ✅ | chromedp | 5045 | 5448 | kernel - How can I tem... | 1 | 3 |
| 32 | easy | forum | https://users.rust-lang.org/t/the-3d-mental-bor... | ✅ | chromedp | 23089 | 23238 | The 3d mental borrow c... | 0 | 0 |
| 33 | medium | forum | https://discuss.huggingface.co/t/valueerror-una... | ✅ | jina | 10708 | 15904 | ValueError: Unable to ... | 1 | 0 |
| 34 | easy | forum | https://discuss.python.org/t/pep-703-making-the... | ✅ | chromedp | 9251 | 20727 | PEP 703 (Making the Gl... | 0 | 0 |
| 35 | easy | forum | https://forums.swift.org/t/accepted-se-0309-unl... | ✅ | jina | 8528 | 5497 |  | 0 | 0 |
| 36 | easy | forum | https://forum.djangoproject.com/t/can-not-optim... | ✅ | chromedp | 10319 | 12101 | Can not optimise "N+1 ... | 0 | 12 |
| 37 | hard | forum | https://github.com/vercel/next.js/discussions/5... | ✅ | utls | 1163 | 589 | Fetch caching in Serve... | 0 | 1 |
| 38 | easy | landing | https://www.figma.com/design/ | ✅ | utls | 724 | 3315 | Free Design Tool for W... | 0 | 0 |
| 39 | medium | landing | https://www.shopify.com/plus | ✅ | utls | 1025 | 4231 | Shopify Plus Platform ... | 0 | 0 |
| 40 | medium | landing | https://www.atlassian.com/software/jira | ✅ | utls | 1684 | 2607 | Jira | Project Managem... | 0 | 0 |
| 41 | medium | landing | https://vercel.com/ | ✅ | jina | 4399 | 17798 | Vercel: Build and depl... | 1 | 0 |
| 42 | hard | landing | https://www.canva.com/enterprise/ | ✅ | chromedp | 8988 | 7556 | Canva Enterprise - you... | 0 | 0 |
| 43 | hard | mixed | https://www.speedtest.net/global-index | ✅ | utls | 621 | 32020 | Speedtest Global Index... | 0 | 0 |
| 44 | hard | mixed | https://excalidraw.com/ | ✅ | jina | 4875 | 718 |  | 0 | 0 |
| 45 | hard | mixed | https://squoosh.app/ | ✅ | utls | 623 | 352 | Squoosh | 0 | 0 |
| 46 | easy | wiki | https://en.wikipedia.org/wiki/List_of_HTTP_stat... | ✅ | jina | 4588 | 31985 | List of HTTP status co... | 2 | 0 |
| 47 | easy | wiki | https://ja.wikipedia.org/wiki/Unicode | ✅ | jina | 13825 | 31964 |  | 0 | 0 |
| 48 | medium | wiki | https://developer.mozilla.org/en-US/docs/Web/HT... | ✅ | jina | 2481 | 32019 | Cache-Control header -... | 1 | 3 |
| 49 | easy | wiki | https://handwiki.org/wiki/Trie | ✅ | chromedp | 19374 | 23916 | Trie - HandWiki | 0 | 0 |
| 50 | medium | wiki | https://www.dictionary.com/browse/unicode | ✅ | utls | 359 | 5483 | UNICODE Definition & M... | 0 | 0 |

## Noise Offenders (noise > 0)

- **https://arxiv.org/abs/1706.03762** (noise=1): nav_breadcrumb
- **https://overreacted.io/a-complete-guide-to-useeffect/** (noise=1): nav_breadcrumb
- **https://gitlab.com/gitlab-org/gitlab-foss/blob/master/REA...** (noise=1): nav_breadcrumb
- **https://bitbucket.org/tildeslash/monit/src/master/** (noise=2): subscribe_cta, related_articles
- **https://developer.mozilla.org/en-US/docs/Web/HTTP/Referen...** (noise=1): nav_breadcrumb
- **https://docs.stripe.com/webhooks** (noise=2): subscribe_cta, related_articles
- **https://stackoverflow.com/questions/11227809/why-is-proce...** (noise=1): nav_breadcrumb
- **https://askubuntu.com/questions/318315/how-can-i-temporar...** (noise=1): login_prompt
- **https://discuss.huggingface.co/t/valueerror-unable-to-cre...** (noise=1): nav_breadcrumb
- **https://vercel.com/** (noise=1): nav_breadcrumb
- **https://en.wikipedia.org/wiki/List_of_HTTP_status_codes** (noise=2): nav_breadcrumb, login_prompt
- **https://developer.mozilla.org/en-US/docs/Web/HTTP/Referen...** (noise=1): nav_breadcrumb
