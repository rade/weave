#! /bin/bash

. "$(dirname "$0")/config.sh"

start_suite "Test docker restart policy"

weave_on $HOST1 launch
assert "docker_on $HOST1 inspect -f '{{.HostConfig.RestartPolicy.Name}}' weave" "always"
assert_raises "check_restart $HOST1 weave"

# stop + launch tests that restart policy changes result
# in the old containers being removed and new ones created
weave_on $HOST1 stop
weave_on $HOST1 launch --no-restart
assert "docker_on $HOST1 inspect -f '{{.HostConfig.RestartPolicy.Name}}' weave" "no"
assert_raises "! check_restart $HOST1 weave"

# Relaunch to prevent the `weave stop` in `end_suite`
# timing out trying to remove the plugin network
weave_on $HOST1 launch --no-restart

end_suite
