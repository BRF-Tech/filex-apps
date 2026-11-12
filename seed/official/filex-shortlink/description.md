# Short links

A short address of your choosing for the links filex hands out:
`l.example.com/aB3x9kQ2z` instead of a share's long one, with a click count.

- **Get short link** sits beside filex's own link in the share dialog, on a
  file request and on an upload's answer. The short address becomes the one
  you copy, and a mail filex sends for that link carries it.
- **The address** is yours: a path on filex's own address
  (`files.example.com/l/<code>`), a host of its own (`l.example.com/<code>`),
  or both.
- **The codes** follow a rule an administrator decides once in the setup
  wizard: the alphabet (letters and digits, URL-safe marks, digits, emoji or
  your own), whether case matters and the length. The wizard shows how hard a
  code is to guess and will not save a rule under 45 bits.
- With your permission it also shortens **any web address**, and lets chosen
  people pick a **code by hand** (`l.example.com/offer`).
- **Visitors** are sent on by a live code; the code followed by `+` shows
  where it goes; every code that leads nowhere is the same "not available"
  page, and filex holds back an address that keeps guessing. A click is
  counted only when a person opened the address, not a chat's preview.

The app keeps its setup and links in its own data, never in filex's
database, and erases them when it is removed or set up again. It reads and
writes no file, sends no mail and has no network.

Needs filex 0.60 or later. English and Turkish.

Source and issues:
[github.com/BRF-Tech/filex-shortlink](https://github.com/BRF-Tech/filex-shortlink).
