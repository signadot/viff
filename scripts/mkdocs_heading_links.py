"""mkdocs hook: a heading that is a link is listed in the page's contents as that link.

A landing page names the pages it leads to as headings, each heading the link to its page, so
that the site's left column, which is the page's table of contents, lists them. The theme lists
every heading by its anchor on the page, which takes a reader who clicks "Data" to the heading
rather than to the data page. This lists a heading whose whole text is one link as the link's
target instead. Any other heading keeps its anchor.
"""

import re

from mkdocs.structure.toc import AnchorLink

# A heading whose text is one link, then the permalink the toc extension appends.
LINKED = re.compile(
    r'<h[1-6] id="([^"]+)"[^>]*>\s*<a href="([^"#][^"]*)">[^<]*</a>\s*'
    r'(?:<a class="headerlink"[^>]*>[^<]*</a>\s*)?</h[1-6]>'
)


class _ToTarget(AnchorLink):
    """A contents entry whose link is a page, not an anchor on this one."""

    def __init__(self, item, href):
        super().__init__(item.title, item.id, item.level)
        self.children = item.children
        self._href = href

    @property
    def url(self):
        return self._href


def _relink(items, targets):
    for i, item in enumerate(items):
        _relink(item.children, targets)
        if item.id in targets:
            items[i] = _ToTarget(item, targets[item.id])


def on_page_content(html, page, config, files):
    targets = dict(LINKED.findall(html))
    if targets:
        _relink(page.toc.items, targets)
    return html
