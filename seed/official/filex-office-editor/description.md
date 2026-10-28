# Office editor

ONLYOFFICE's editor as a filex app, **without a Document Server**. Word, Excel and PowerPoint
documents and their kin - `.docx`, `.xlsx`, `.pptx`, the OpenDocument `.odt`, `.ods`, `.odp`,
the older `.doc`, `.xls`, `.ppt`, `.rtf`, `.csv`, the templates and the macro-enabled kinds, 45
in all - open in the editor inside filex, in place of the preview. Since 0.4.0 the app also
brings filex an **office engine for the server**, so filex draws office thumbnails and converts
office documents without a Document Server too: an installation can delete its Document Server
container.

- **Edit and save.** Save (the editor's button, Ctrl+S, or filex's Save) writes a new version of
  the file in its own format; while there are changes it also saves every ten minutes, and filex
  asks before you leave unsaved work. A format the converter cannot write (`.doc`, `.xls`,
  `.ppt` and a few more) is saved beside the file as `.docx`, `.xlsx` or `.pptx`; a document
  somebody changed while it was open keeps their version and gets yours beside it as a conflict
  copy.
- **Edit together.** Everyone who has the document open edits the same document and sees the
  others' cursors, locks and changes as they make them, through filex's relay: filex seals what
  they share and puts it in one order, the app holds no key and talks to no server. Someone who may
  only read follows the changes live.
- **Encrypted folders.** A document in an encrypted folder or vault you have unlocked opens too:
  your browser decrypts it for the editor and encrypts the save before it leaves, so the server
  sees only encrypted data. Editing together works there as well.
- **csv, passwords, spelling, macros.** A csv keeps its own delimiter and encoding when it is
  saved; a password-protected document opens with its password and stays protected; spelling and
  hyphenation use the dictionaries of filex's languages, and an administrator adds 43 more with one
  click; macros run after the editor asks.
- **Download as and Print.** The File menu's Download as writes DOCX, ODT, RTF, TXT, XLSX, ODS,
  CSV (in every code page of the editor's list), PPTX, ODP, their templates, PDF and PDF/A in your
  browser, and filex hands you the file. Print hands filex a PDF; filex opens the print dialog
  once you allow it.
- **New document.** filex's New document dialog gets six rows: a document, a spreadsheet and a
  presentation, as OOXML and as OpenDocument.
- **Your settings stay.** Zoom, units, the ribbon folded or not and the hints you closed are kept
  between openings. The editor follows filex's light or dark theme and your language, or the
  editor language an administrator fixed.
- **On a phone** the document opens in ONLYOFFICE's phone view to read, and **Edit** opens the
  editor with it.

**The office engine.** ONLYOFFICE's converter, x2t, and the editor's own page layout, both
compiled to WebAssembly, come with the release as `engine.zip`. filex runs them on the server in a
process of its own, with only the documents it hands them and no network, for office thumbnails
and for the conversions apps ask for (the Convert app, MCP's `file_convert`). filex prepares the
engine once after the install, in the background, a few minutes; **Default apps → Office engine**
shows when it is ready.

The editor itself runs in your browser: its files come from ONLYOFFICE's official Document Server
image (ONLYOFFICE Docs 9.4.0), and documents are converted for it in your browser by the same x2t,
which the project builds from ONLYOFFICE core 9.4.0. filex serves the app's package and runs it in
a sandboxed frame; the app reads and saves only the file it was opened with.

Needs filex 0.57 or later; on filex 0.56 the store offers 0.3.1, which edits the six OOXML and
OpenDocument kinds and brings no engine, and on filex 0.55 0.1.1. The app's own texts are in
English and Turkish; the editor speaks the languages ONLYOFFICE ships.

Based on ONLYOFFICE Docs by Ascensio System SIA. ONLYOFFICE® and ONLYOFFICE Docs™ are registered
trademarks or trademarks of Ascensio System SIA; this app is not affiliated with or endorsed by
Ascensio System SIA. The app is free software under the GNU AGPL version 3 or later.

Source and issues:
[github.com/BRF-Tech/filex-office-editor](https://github.com/BRF-Tech/filex-office-editor).
