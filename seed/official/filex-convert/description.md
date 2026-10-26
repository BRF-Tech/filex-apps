# Convert

Right-click one or more files, pick a target format, get the converted files next to the originals.
Convert runs as a WebAssembly module inside filex: no service to deploy, no port to open.

- **73 formats in 9 categories**: documents (Markdown, HTML, Word, ODT, RTF, PowerPoint, EPUB, PDF),
  images, video, audio, e-books, archives, data (CSV, JSON, YAML, XML, TOML, Excel), subtitles and
  fonts. A conversion may chain up to three steps (`pptx` to `pdf` to `png`).
- **Most of it needs no engine at all**: documents, images, e-books, data, archives, subtitles and
  fonts are converted in pure Go inside the sandbox.
- **Engines when the server has them**: video, audio, HEIC/AVIF, SVG rendering, PDF pages and
  layout-faithful office exports use the server's ffmpeg, ImageMagick, Ghostscript, poppler and rsvg,
  and office documents go through the ONLYOFFICE Document Server connected to filex. Each engine is
  a separate permission, and an administrator sees which targets a missing engine would unlock.
- **Several files at once**: combine images into one PDF, or files into one archive.

Since 0.2.0 it needs filex 0.50 or later. Spanish, German and French are AI-translated, awaiting
review by a native speaker - corrections welcome.

Source, the full conversion matrix and issues:
[github.com/BRF-Tech/filex-convert](https://github.com/BRF-Tech/filex-convert).
