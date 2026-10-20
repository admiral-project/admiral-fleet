# Uploaded backup restore trust boundary

Harbor serves customer-uploaded backups through a short-lived URL containing a
customer ID, expiry, and HMAC signature. Harbor verifies that signature when
the download is requested. Admirald validates the restore request and gives
Fleet a narrowly scoped capability for the exact Harbor HTTPS origin, upload
download path, pinned portal addresses, and Admiral CA.

Fleet does not have Harbor's HMAC signing key and does not verify the signature
cryptographically. It requires the signed URL fields to be present, then checks
the trusted origin, exact path shape, resolved IPs, TLS certificate, redirects,
and downloaded object checksum. Fleet must keep rejecting arbitrary private
addresses and URLs outside the capability supplied by Admirald.
