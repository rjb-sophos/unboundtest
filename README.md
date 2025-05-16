# Unbound test server

This is a little HTTP server that makes it easy to test DNS lookups without
running your own Unbound instance, and get detailed logs. Useful for debugging
DNS issues with Let's Encrypt. See index.html for more details.

THis is a fork of the original unboundtest project from https://github.com/jsha/unboundtest

To run locally:

```
go run unboundtest.go
```

Then visit http://localhost:1232/.

Alternately:

```
docker build . --tag unboundtest
docker run unboundtest
```

Then use `docker ps` and `docker inspect` to find the IP address of the
unboundtest container, and visit that IP address on port 1232.

## CLI
```
Usage of unboundtest:
  -listen string
        The address on which to listen for incoming Web requests (default ":1232")
  -unboundAddress string
        The address the unbound.conf instructs Unbound to listen on (default "127.0.0.1:1053")
  -unboundConfig string
        The path to the unbound.conf file (default "unbound.conf")
  -unboundConfigNoV6
        The path to an unbound.conf file that disables IPv6, used when that option is selected on the web page (default "unbound-noV6.conf")
  -unboundExec string
        The path to the unbound executable (default "unbound")
  -index string
        The path to the index.html (default "index.html")

```
