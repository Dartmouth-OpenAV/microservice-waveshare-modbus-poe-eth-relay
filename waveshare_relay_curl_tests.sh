#!/bin/bash

# Waveshare Relay Microservice API Test Script
# This script exercises all endpoints from the Waveshare Relay Postman collection.
# For an automated pass/fail test against real hardware, see tests/live_test_loop.py

# Configuration variables - Update these values as needed
MICROSERVICE_URL="localhost:8080"
DEVICE_FQDN="10.8.80.12"   # append :502 if the device is set to Modbus TCP mode
RELAY_NUMBER_1="1"
RELAY_NUMBER_2="2"
RELAY_NUMBER_3="3"

echo "Starting Waveshare Relay Microservice API Tests..."
echo "Microservice URL: $MICROSERVICE_URL"
echo "Device FQDN: $DEVICE_FQDN"
echo "Test Relays: $RELAY_NUMBER_1, $RELAY_NUMBER_2, $RELAY_NUMBER_3"
echo "=============================================="

# GET healthcheck (the first GET of any endpoint returns 204 until the microservice has polled the device)
echo "Testing GET healthcheck..."
curl -X GET "http://$MICROSERVICE_URL/$DEVICE_FQDN/healthcheck"
sleep 1
curl -X GET "http://$MICROSERVICE_URL/$DEVICE_FQDN/healthcheck"
echo

echo "Testing GET states (all relays)..."
curl -X GET "http://$MICROSERVICE_URL/$DEVICE_FQDN/states"
sleep 1
curl -X GET "http://$MICROSERVICE_URL/$DEVICE_FQDN/states"
echo

# GET State for each relay
for RELAY in $RELAY_NUMBER_1 $RELAY_NUMBER_2 $RELAY_NUMBER_3; do
    echo "Testing GET State for Relay $RELAY..."
    curl -X GET "http://$MICROSERVICE_URL/$DEVICE_FQDN/state/$RELAY"
    sleep 1
done

echo "=============================================="
echo "Starting SET State operations..."
echo "=============================================="

for RELAY in $RELAY_NUMBER_1 $RELAY_NUMBER_2 $RELAY_NUMBER_3; do
    echo "Testing SET State to 'on' for Relay $RELAY..."
    curl -X PUT "http://$MICROSERVICE_URL/$DEVICE_FQDN/state/$RELAY" \
         -H "Content-Type: application/json" \
         -d "\"on\""
    sleep 1
done

for RELAY in $RELAY_NUMBER_1 $RELAY_NUMBER_2 $RELAY_NUMBER_3; do
    echo "Testing SET State to 'off' for Relay $RELAY..."
    curl -X PUT "http://$MICROSERVICE_URL/$DEVICE_FQDN/state/$RELAY" \
         -H "Content-Type: application/json" \
         -d "\"off\""
    sleep 1
done

echo "Testing SET All States to 'on'..."
curl -X PUT "http://$MICROSERVICE_URL/$DEVICE_FQDN/allstates" \
     -H "Content-Type: application/json" \
     -d "\"on\""
sleep 1

echo "Testing SET All States to 'off'..."
curl -X PUT "http://$MICROSERVICE_URL/$DEVICE_FQDN/allstates" \
     -H "Content-Type: application/json" \
     -d "\"off\""
sleep 1

echo "=============================================="
echo "Starting SET Trigger operations..."
echo "=============================================="

echo "Testing SET Trigger for Relay $RELAY_NUMBER_1 (1 second)..."
curl -X PUT "http://$MICROSERVICE_URL/$DEVICE_FQDN/trigger/$RELAY_NUMBER_1" \
     -H "Content-Type: application/json" \
     -d "\"1\""
sleep 2

echo "Testing SET Trigger for Relay $RELAY_NUMBER_2 (2 seconds)..."
curl -X PUT "http://$MICROSERVICE_URL/$DEVICE_FQDN/trigger/$RELAY_NUMBER_2" \
     -H "Content-Type: application/json" \
     -d "\"2\""
sleep 3

echo "Testing SET Trigger for Relay $RELAY_NUMBER_3 (0.5 seconds)..."
curl -X PUT "http://$MICROSERVICE_URL/$DEVICE_FQDN/trigger/$RELAY_NUMBER_3" \
     -H "Content-Type: application/json" \
     -d "\"0.5\""
sleep 1

echo "=============================================="
echo "Starting SET Timed Trigger operations..."
echo "=============================================="

echo "Testing SET Timed Trigger (Relay $RELAY_NUMBER_1 -> Relay $RELAY_NUMBER_2, 5 seconds)..."
curl -X PUT "http://$MICROSERVICE_URL/$DEVICE_FQDN/timedtrigger/$RELAY_NUMBER_1/$RELAY_NUMBER_2" \
     -H "Content-Type: application/json" \
     -d "\"5\""
sleep 7

echo "Testing SET Timed Trigger (Relay $RELAY_NUMBER_2 -> Relay $RELAY_NUMBER_1, 10 seconds)..."
curl -X PUT "http://$MICROSERVICE_URL/$DEVICE_FQDN/timedtrigger/$RELAY_NUMBER_2/$RELAY_NUMBER_1" \
     -H "Content-Type: application/json" \
     -d "\"10\""
sleep 12

echo "=============================================="
echo "Final state check - Getting current states and errors..."
echo "=============================================="

curl -X GET "http://$MICROSERVICE_URL/$DEVICE_FQDN/states"
echo
curl -X GET "http://$MICROSERVICE_URL/$DEVICE_FQDN/errors"
echo

echo "=============================================="
echo "All Waveshare Relay API tests completed!"
echo "=============================================="
