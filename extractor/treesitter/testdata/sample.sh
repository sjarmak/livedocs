#!/bin/bash
# Sample shell script for testing.

source /etc/profile
. ~/.bashrc

greet() {
    local name="$1"
    echo "Hello, ${name}!"
}

cleanup() {
    rm -rf /tmp/test
}

# Call the function
greet "world"
