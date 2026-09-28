"""mkdocs hook: a link into a page the site does not publish goes to the page in the repository.

The docs are read in three places: on the site, in the repository by a person, and in the
repository by an agent. A page the site excludes (mkdocs.yml, exclude_docs) is still in the
repository, and a relative link to it is right for the two readers there. On the site that
link would 404, so this rewrites it, at build time only, to the file on GitHub. The source
keeps the relative link.

A link inside a fenced code block is left alone.
"""

import os
import posixpath
import re

LINK = re.compile(r"\]\(([^)\s#]+)(#[^)\s]*)?\)")
FENCE = re.compile(r"^(```|~~~)", re.M)


def _rewrite(text, page, files, base, docs_dir):
    here = posixpath.dirname(page.file.src_uri)

    def repl(m):
        target, anchor = m.group(1), m.group(2) or ""
        if "://" in target or target.startswith("mailto:"):
            return m.group(0)
        path = posixpath.normpath(posixpath.join(here, target))
        if path.startswith(".."):
            return m.group(0)
        f = files.get_file_from_path(path)
        if f is not None and not f.inclusion.is_excluded():
            return m.group(0)
        # Only what is in the repository: a link to nothing is left for mkdocs to report.
        if not os.path.exists(os.path.join(docs_dir, path)):
            return m.group(0)
        return f"]({base}/{path}{anchor})"

    return LINK.sub(repl, text)


def on_page_markdown(markdown, page, config, files):
    base = config["repo_url"].rstrip("/") + "/blob/main/docs"
    parts = FENCE.split(markdown)
    out, in_fence, i = [], False, 0
    # FENCE.split keeps each fence marker as its own element: text, marker, text, marker, ...
    while i < len(parts):
        chunk = parts[i]
        if i % 2 == 1:
            out.append(chunk)
            in_fence = not in_fence
        else:
            out.append(chunk if in_fence else _rewrite(chunk, page, files, base, config["docs_dir"]))
        i += 1
    return "".join(out)
