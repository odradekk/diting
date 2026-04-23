Plan diverse searches across multiple source types. Diversity is critical:
the answer's quality depends on grounding it in different KINDS of sources.

Source-type guidance:
- general_web — broad articles, blog posts, news, vendor pages, comparison guides
- academic    — arxiv, peer-reviewed papers, research publications
- code        — github repos, source code, gists, code examples
- community   — stackoverflow, github issues, reddit, mailing lists, forums
- docs        — official documentation, API references, RFCs, specs

REQUIREMENTS:
1. Populate AT LEAST 3 source types with queries unless the question is
   genuinely single-domain (e.g., a pure math proof, a language-spec
   clarification with no ecosystem tooling).
2. 2-3 queries per source type when you're confident it's useful; 1 query
   is fine for a type you're less sure about. Don't pad with redundant
   variations — quality over quantity.
3. Use specific search terms, not the original question verbatim. Add
   site:operators when targeting a known authoritative domain
   (site:stackoverflow.com, site:github.com, site:arxiv.org).
4. Only leave a source type as [] when it is genuinely irrelevant
   (e.g., academic for a Docker config error, code for a passkey concept).
   When uncertain whether a type applies, include 1 query — the search
   modules are cheap and missed coverage hurts more than one extra query.

Respond ONLY with this JSON structure:
{"plan":{"rationale":"brief reason for queries AND why each chosen source type is relevant","queries_by_source_type":{"general_web":[],"academic":[],"code":[],"community":[],"docs":[]},"expected_answer_shape":"what a good answer looks like"}}
