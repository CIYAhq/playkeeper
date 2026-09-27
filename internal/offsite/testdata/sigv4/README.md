# Signature Version 4 test vectors

These directories are copied unchanged from the AWS Common Runtime's signing
test suite, `tests/aws-signing-test-suite/v4/` in
[awslabs/aws-c-auth](https://github.com/awslabs/aws-c-auth) at commit
`c4bc791ac6985eedb503e882cd450cc5b344c2f2`. They are licensed under the
Apache License 2.0; see `LICENSE` and `NOTICE` in this directory.

Each directory holds a request (`request.txt`), the signing context
(`context.json`) and the expected canonical request, string to sign and
signature. `sigv4_test.go` checks all of them.

Left out: the seven `*-normalized` vectors, because S3 does not normalize
paths (their `*-unnormalized` twins are included), and the three session
token vectors (`get-vanilla-with-session-token`, `post-sts-header-*`),
because copies off the server are signed with an access key and secret
only.
