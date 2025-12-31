# Contributing to Laplaced

Hey! 👋 Thanks for checking out Laplaced. Contributions are super welcome!

There are no strict rules here, just some common sense stuff to keep things moving smoothly.

## Found a Bug? 🐛

If something's broken, please report it! Open an [Issue](https://github.com/runixer/laplaced/issues) and describe:
1.  What you did.
2.  What happened.
3.  What you expected to happen.
4.  Logs or screenshots are always a plus!

## Want to Add a Feature? 🚀

Awesome! If it's a big change, maybe open an Issue first to discuss it. If it's something small or obvious, feel free to jump straight to a Pull Request.

## How to Submit Code

1.  **Fork** the repo.
2.  Create a **branch** for your cool new thing.
3.  Write some code. Try to keep it clean and "Go-like".
4.  Make sure it works:
    ```bash
    go test ./...
    ```
5.  Format it (because nobody likes messy code):
    ```bash
    go fmt ./...
    ```
6.  Send a **Pull Request**!

## Quick Tips

- Check [`internal/config/default.yaml`](internal/config/default.yaml) to see how things are configured.
- The main magic happens in `cmd/bot/main.go` and `internal/bot/`.

Thanks for being awesome! 🤘
