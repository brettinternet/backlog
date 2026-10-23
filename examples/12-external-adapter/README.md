# External adapter

Any tracker `bl` doesn't support, through a program that speaks JSON-RPC on
stdin and stdout. [`adapter.py`](adapter.py) turns each checkbox in
[`TODO.md`](TODO.md) into a read-only item.

```sh
bl adapter check python3 adapter.py
bl trust
bl next -n 0
```

```text
Adapter protocol v1: PASS
T-2  todo  Write the landing page
T-3  todo  Set up the mailing list
```

An adapter offers only the capabilities it lists. This one lists `list` and
`read`, so writes are refused:

```text
$ bl set todo:T-2 status=done
bl: unsupported operation set
```

`bl trust` approves running the adapter, which has your file and network
access. It gets a minimal environment with no provider tokens. See the
[protocol](../../docs/external-adapter-protocol.md) for writes, paging, and
errors.
