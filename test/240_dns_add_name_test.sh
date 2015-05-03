#! /bin/bash

. ./config.sh

C1=10.2.0.78
C2=10.2.0.34
NAME=seetwo.weave.local

start_suite "Add and remove names on a single host"

weave_on $HOST1 launch-dns 10.2.254.1/24

weave_on $HOST1 run $C2/24 -t --name=c2 ubuntu
weave_on $HOST1 run --with-dns $C1/24 -t --name=c1 aanand/docker-dnsutils /bin/sh

weave_on $HOST1 dns-add $C2/24 c2 -h $NAME

assert_dns_record $HOST1 c1 $NAME $C2

weave_on $HOST1 dns-remove $C2/24 c2

assert_raises "exec_on $HOST1 c1 getent hosts $C2 | grep $NAME"

end_suite
