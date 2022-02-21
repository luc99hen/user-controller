#!/bin/bash

# 脚本出错时终止执行
set -e

git clone https://github.com/openyurtio/openyurt.git
cd openyurt
make WHAT=cmd/yurtctl

_output/bin/yurtctl join [openyurt集群ip]:6443 --token=[join-token] --node-type=edge-node --discovery-token-unsafe-skip-ca-verification --v=5