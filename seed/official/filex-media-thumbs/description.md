# Media Thumb Engine

Thumbnails in every view of the explorer, and a preview of their own, for files filex cannot draw by
itself:

- **Photoshop** `.psd`, `.psb`: the merged image of the document, else the thumbnail Photoshop saves;
  the preview adds size, color mode, bit depth, channels and layers.
- **Affinity** `.af` (Affinity 3), `.afphoto`, `.afdesign`, `.afpub` (Affinity 1 and 2): the PNG
  Affinity saves inside the file.
- **Pixelmator Pro** `.pxd` (the single-file ZIP of Pixelmator Pro 2.4 and later): the QuickLook
  preview inside it. A text `.pxd` (a Cython declaration file) is left to filex.
- **Fonts** `.ttf`, `.otf`, `.woff`, `.woff2`: the font drawn with itself; the preview shows a sample
  text you can change at six sizes, its weights, every character it draws and its names.

No design file is opened as a document: each program saves a flattened picture inside the file, and
that picture is what the app reads, so it needs no Photoshop, Affinity or Pixelmator on the server.
It asks for one permission, `files:read`, writes nothing and has no network. Without it, filex is as
it was: these files keep their type icons.

Needs filex 0.50 or later. English and Turkish.

Source and issues:
[github.com/BRF-Tech/filex-media-thumbs](https://github.com/BRF-Tech/filex-media-thumbs).
