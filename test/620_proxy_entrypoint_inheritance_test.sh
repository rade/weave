#! /bin/bash

. ./config.sh

start_suite "Proxy uses entrypoint from the container with weavewait"
# Setup and sanity-check
weave_on $HOST1 launch-proxy
docker_on $HOST1 build -t inspect-ethwe - <<- EOF
  FROM gliderlabs/alpine
  ENTRYPOINT ["ip", "link", "show", "ethwe"]
EOF

# Boot a new container with no entrypoint of its own, just a command
assert_raises "docker_proxy_on $HOST1 run -e 'WEAVE_CIDR=10.2.1.1/24' inspect-ethwe | grep 'state UP'"

end_suite
