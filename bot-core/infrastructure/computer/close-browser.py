"""Ask Chromium to flush its profile before the container stops."""
import json
import sys
from urllib.request import urlopen

import websocket

try:
    with urlopen("http://127.0.0.1:9223/json/version", timeout=3) as response:
        address = json.load(response)["webSocketDebuggerUrl"]
    connection = websocket.create_connection(address, timeout=3, suppress_origin=True)
    try:
        connection.send(json.dumps({"id": 1, "method": "Browser.close"}))
        response = json.loads(connection.recv())
        if "error" in response:
            raise RuntimeError("Browser.close rejected")
    finally:
        connection.close()
except Exception as error:
    print(f"Chromium close failed: {type(error).__name__}", file=sys.stderr)
    sys.exit(1)
