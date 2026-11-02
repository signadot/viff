# docs site: the verse logo, favicon and link card

The docs site carries the mkdocs theme's default favicon and the site's name as text in its bar, and a link to it unfurls with no image.

`docs/images/` holds the identity: the mark and the wordmark as SVG (outlined, no text elements), light and dark wordmarks, the favicon as SVG and .ico, and a 1280×640 card.

The site takes them where the theme has a seam for each (`overrides/main.html`):

- the favicon, SVG first and .ico for browsers that do not read one (`site_meta`);
- the light wordmark as the bar's brand, since the bar is bootstrap's primary blue (`site_name`), sized by `docs/extra.css`;
- the card as `og:image`, with the title, description and page address beside it (`extrahead`), which needs `site_url` in `mkdocs.yml` for an absolute address — and `site_description`, which the theme also prints as the home page's description.

The repository's own social preview is the same card, set in the repository's settings by hand; not part of this.