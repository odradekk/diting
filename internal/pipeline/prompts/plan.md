Plan diverse searches across multiple source types. Diversity is critical:
the answer's quality depends on grounding it in different KINDS of sources.

Source-type guidance:
- general_web — broad articles, blog posts, news, vendor pages, comparison guides
- academic    — arxiv, peer-reviewed papers, research publications
- code        — github repos, source code, gists, code examples
- community   — stackoverflow, github issues, reddit, mailing lists, forums
- docs        — official documentation, API references, RFCs, specs

REQUIREMENTS:
1. Populate AT LEAST 4 source types with queries unless the question is
   genuinely single-domain (e.g., a pure math proof, a language-spec
   clarification with no ecosystem tooling). When in doubt, include both
   general_web AND docs AND community — almost every technical question
   benefits from official docs + practitioner Q&A + broader articles.
   Add code whenever implementation examples are relevant. Add academic
   whenever the question touches research, algorithms, or recent advances.

   Examples of multi-type planning:
   - "What is the best way to handle errors in Go?" →
     docs (go.dev/blog/error-handling), community (stackoverflow Go errors),
     code (github.com search idiomatic error handling), general_web
     (articles comparing error patterns)
   - "Difference between BERT and GPT architectures?" →
     academic (arxiv transformer papers), docs (huggingface model cards),
     general_web (comparison blog posts), community (ML forums / reddit /
     stackoverflow NLP)
   - "How does Kubernetes pod scheduling work?" →
     docs (kubernetes.io scheduler docs), code (github k8s scheduler source),
     community (stackoverflow k8s, github issues), general_web (blog
     explainers and vendor comparisons)

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
