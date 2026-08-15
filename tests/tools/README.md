# Test tools

Small clients written because the thing they talk to had no other way to be
reached from a test.

## `smbcheck.py`

Enough of an SMB2 client to prove somebody can open a Homebase share:
NEGOTIATE, SESSION_SETUP with NTLMv2, TREE_CONNECT.

```sh
python3 tests/tools/smbcheck.py homebase.local backup hbshare-okan 'the-password'
```

It exists because neither machine involved in testing Milestone 12 had
`smbclient` and neither could install one. The alternative was to declare file
sharing working because the port was open and `testparm` was happy — which is
the shape of every bug in [the testing notes](../../docs/development/testing.md).

Assert the failures too. A wrong password and an account that does not exist
must both be refused, and a check that only ever runs with the right password
cannot tell you that they are.
