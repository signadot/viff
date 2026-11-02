# docs: the site's favicon is the .ico alone; Safari showed the SVG blank

With one .ico and the SVG beside it (fdfc87ca1), Safari 26.5 on signadot.github.io/verse still showed no icon. The SVG favicon is drawn with masks and gradients, and a browser that takes an SVG icon over the .ico shows it blank. The head names the .ico alone, at 16, 32 and 48, and the touch icon.