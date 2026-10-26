# draw.io

[draw.io](https://www.drawio.com) as a filex app. `.drawio` and `.dio` files open in the draw.io
editor, inside filex, in place of the preview. **Save** writes a new version of the file, and filex's
**New document** dialog gets a **draw.io diagram** row.

It is an app with its own interface and no module: nothing of it runs on the filex server. filex
serves draw.io's own files from the app's package and runs them in a sandboxed frame. The editor
reads and saves only the file it was opened with, and loads its translations, shapes and templates
from its own package only.

The version number follows draw.io's (31.5.3). This app is MIT-licensed; draw.io is Apache 2.0.

Source and issues: [github.com/BRF-Tech/filex-drawio](https://github.com/BRF-Tech/filex-drawio).
