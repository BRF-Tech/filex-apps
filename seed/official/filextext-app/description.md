# filextext

An **end-to-end encrypted text workspace** in a single `.fxtxt` file. Pages and folders on the left,
tabs on top, and AFFiNE's BlockSuite editor in the middle: headings, lists, to-dos, code with
highlighting, tables, callouts, LaTeX, images, links between pages, Markdown import and export.

Everything is encrypted **in your browser**. filex stores ciphertext and never sees the password,
the recovery key, the keys, a page title or a word of the text. Lose both the password and the
recovery key and nobody can open the workspace - not filex, not an administrator.

It is an app with its own interface and no module: nothing of it runs on the filex server. It reads
only the file it was opened with and saves it back as a new version.

Source, the file format and the security model:
[github.com/BRF-Tech/filextext-app](https://github.com/BRF-Tech/filextext-app).
