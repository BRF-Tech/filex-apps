# Office editor

ONLYOFFICE's editor as a filex app, **without a Document Server**. Word, Excel and PowerPoint
documents - `.docx`, `.xlsx`, `.pptx` and the OpenDocument `.odt`, `.ods`, `.odp` - open in the
editor inside filex, in place of the preview.

- **Edit and save.** Save (the editor's button, Ctrl+S, or filex's Save) writes a new version of
  the file in its own format; while there are changes it also saves every ten minutes, and filex
  asks before you leave unsaved work.
- **Edit together.** Everyone who has the document open edits the same document and sees the
  others' cursors, locks and changes as they make them, through filex's relay: filex seals what
  they share and puts it in one order, the app holds no key and talks to no server. Someone who may
  only read follows the changes live.
- **Encrypted folders.** A document in an encrypted folder or vault you have unlocked opens too:
  your browser decrypts it for the editor and encrypts the save before it leaves, so the server
  sees only encrypted data. Editing together works there as well.
- **Download as and Print.** The File menu's Download as writes DOCX, ODT, RTF, TXT, XLSX, ODS,
  CSV, PPTX, ODP, their templates, PDF and PDF/A in your browser, and filex hands you the file. Print hands
  filex a PDF; filex opens the print dialog once you allow it.
- **New document.** filex's New document dialog gets three rows: a document, a spreadsheet and a
  presentation.
- **Your settings stay.** Zoom, units, the ribbon folded or not and the hints you closed are kept
  between openings. The editor follows filex's light or dark theme and your language.
- **On a phone** the document opens in ONLYOFFICE's phone view to read, and **Edit** opens the
  editor with it.

Nothing of it runs on the filex server. The editor's files come from ONLYOFFICE's official
Document Server image (ONLYOFFICE Docs 9.4.0), and documents are converted in your browser by
ONLYOFFICE's converter, x2t, which the project builds from ONLYOFFICE core 9.4.0 to
WebAssembly. filex serves the app's package and runs it in a sandboxed frame; the app reads and
saves only the file it was opened with.

Needs filex 0.56 or later; on filex 0.55 the store offers 0.1.1, which edits alone and not in
encrypted folders. The app's own texts are in English and Turkish; the editor speaks the
languages ONLYOFFICE ships.

Based on ONLYOFFICE Docs by Ascensio System SIA. ONLYOFFICE® and ONLYOFFICE Docs™ are registered
trademarks or trademarks of Ascensio System SIA; this app is not affiliated with or endorsed by
Ascensio System SIA. The app is free software under the GNU AGPL version 3 or later.

Source and issues:
[github.com/BRF-Tech/filex-office-editor](https://github.com/BRF-Tech/filex-office-editor).
