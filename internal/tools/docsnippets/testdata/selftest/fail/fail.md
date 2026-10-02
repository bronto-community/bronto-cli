# Snippet tester self-test (failing)

Every block below must FAIL; TestSelfTestGoesRed asserts how.

Wrong expected output:

```console test
$ bronto collections list -o table
COLLECTION  DATASETS  NAMES
checkout-service  prod
```

A command that exits non-zero:

```console test
$ bronto search "status >= 500"
```

exit=3 expected, but the command succeeds:

```console test exit=3
$ bronto datasets list -o table
```

A wildcard cannot hide a wrong line:

```console test
$ bronto collections list -o table
COLLECTION  DATASETS  NAMES
...
prod        4         checkout-service, payments-api, web-frontend
...
```

A tty block gets the table, not the piped JSON:

```console test tty
$ bronto collections list
{"prod":[{"dataset":"checkout-service","id":"0b7c6f2e-1d4a-4c8e-9a51-3f2d8e6b7a10"},{"dataset":"payments-api","id":"5e2a9c41-7b3d-4f6a-8c20-9d1e4b5f6a21"},{"dataset":"web-frontend","id":"8f1d3b7a-2c5e-4a9b-b6d4-1e7f9a3c2b32"}],"staging":[{"dataset":"checkout-service","id":"c3a8e5d2-9f4b-4e1a-a7c3-6b2d8f1e4c43"}]}
```
