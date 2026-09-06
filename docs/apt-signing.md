# Signed APT repository

The repository generator now requires `APT_SIGNING_KEY_ID` and the matching
secret key. It fails closed instead of publishing unsigned metadata.

The persistent signing fingerprint is:

`8D7673280D909B3C12AEE66971FECA053730E85B`

The key was created on 2026-09-06 with a two-year expiry. The private key is
stored in the repository's `APT_SIGNING_PRIVATE_KEY` Actions secret; the
fingerprint is the `APT_SIGNING_KEY_ID` Actions variable. The owner's recovery
copy and revocation certificate are outside this checkout in
`C:\Users\shreyam\.codex\distribution-signing\dbterm`, with inherited Windows
permissions removed. Back this directory up securely; never commit or publish
its contents. Rotate the key before expiry and document migration before
removing the previous public key.

Run **Repair signed APT repository** with an existing stable release tag to
repair an already-published repository. The workflow verifies release checksums,
signs indexes, tests an authenticated APT install/removal, then updates only
`apt/` on `gh-pages`. Normal releases also sign metadata; website deployment
preserves `apt/`.

Publication must be verified before advertising these installation commands:

```sh
curl -fsSLo /tmp/dbterm-key.asc https://dbterm.shreyam1008.com.np/apt/key.asc
gpg --show-keys --with-fingerprint /tmp/dbterm-key.asc
# Compare the full fingerprint above before trusting this repository.
sudo install -m 644 /tmp/dbterm-key.asc /usr/share/keyrings/dbterm.asc
echo 'deb [signed-by=/usr/share/keyrings/dbterm.asc] https://dbterm.shreyam1008.com.np/apt stable main' | sudo tee /etc/apt/sources.list.d/dbterm.list
sudo apt-get update
sudo apt-get install dbterm
```

Do not use `trusted=yes` or `apt-key`. Public verification requires `key.asc`,
`dists/stable/InRelease`, and an authenticated clean-machine install.
