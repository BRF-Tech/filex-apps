# Retro

Plays old games in place of the file preview. Double-click a game file in the explorer and it runs,
inside the sandbox filex draws for every app interface.

- **DOOM-engine player** for `.wad` files (an IWAD, or a PWAD on top of one) and `.fxdoom`, this
  app's save pack.
- **DOS player** for `.jsdos` bundles ([js-dos](https://js-dos.com), DOSBox).

**No game data ships with it.** You bring your own files; [Freedoom](https://freedoom.github.io) is a
free WAD you can download yourself.

**Saved games live inside a file**, because an app interface can only write the file it was opened
with (or a new file where you choose): a DOS game's saves go back into its `.jsdos` bundle as a new
version, and DOOM-engine saves go into a `.fxdoom` pack that holds the WADs, the saves and the
settings.

Source and issues: [github.com/BRF-Tech/filex-retro](https://github.com/BRF-Tech/filex-retro).
