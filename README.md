[![CI Status](https://img.shields.io/github/workflow/status/xuoe/logwrap/CI?style=flat-square)](https://github.com/xuoe/logwrap/actions?query=workflow:CI) [![Latest Release](https://img.shields.io/github/v/release/xuoe/logwrap?style=flat-square)](https://github.com/xuoe/logwrap/releases/latest) [![Changelog](https://img.shields.io/badge/changelog-latest-blue?style=flat-square)](CHANGELOG.md)

logwrap
=======

**logwrap** is a tool that annotates the output of other programs and writes the result to a rotatable logfile. It is inspired by [`rlwrap`](https://github.com/hanslub42/rlwrap), the rotation mechanism of OpenBSD's [newsyslog](https://man.openbsd.org/newsyslog.8), and tools like [`annotate-output`](https://linux.die.net/man/1/annotate-output) and [`ts`](https://linux.die.net/man/1/ts).

Installation
------------

If you have Go installed, `go install github.com/xuoe/logwrap@latest` and ensure `$GOPATH/bin` is part of `$PATH`.

Otherwise, grab a binary for your platform from the [releases page](https://github.com/xuoe/logwrap/releases) and place it somewhere in `$PATH`.

Demo
----

[![asciicast](https://asciinema.org/a/cg6dk2eRJ9h4CBMtY1vEYgF0F.svg)](https://asciinema.org/a/cg6dk2eRJ9h4CBMtY1vEYgF0F)

The demo covers a lot of what `logwrap` does. For more, see `--help`, and note, in particular, the available help topics, which can be used to explore more of `logwrap`s functionality via `--help TOPIC`.

License
-------

**logwrap** is released under the [MIT license](./LICENSE.md).
