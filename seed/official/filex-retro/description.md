# Retro

Plays old games in place of the file preview. Double-click a game file in the explorer and it runs,
inside the sandbox filex draws for every app interface.

- **DOOM-engine player** for `.wad` files (an IWAD: DOOM, DOOM II, Final DOOM, Freedoom or FreeDM)
  and `.fxdoom`, this app's save pack. A PWAD (a mod or a map pack) does not play on its own: it
  needs an IWAD under it, and an app can read only the file you opened.
- **DOS player** for `.jsdos` bundles ([js-dos](https://js-dos.com), DOSBox).

**No game data ships with it.** You bring your own files; [Freedoom](https://freedoom.github.io) is a
free WAD you can download yourself.

**Saved games live inside a file**, because an app interface can only write the file it was opened
with (or a new file where you choose): a DOS game's saves go back into its `.jsdos` bundle as a new
version, and DOOM-engine saves go into a `.fxdoom` pack that holds the IWAD, the saves and the
settings.

Source and issues: [github.com/BRF-Tech/filex-retro](https://github.com/BRF-Tech/filex-retro).
