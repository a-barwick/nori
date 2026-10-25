#!/usr/bin/env python3
"""Terraform external data: reject unsafe explicit host deadlines without cloud I/O."""
import json
import sys
from datetime import datetime, timedelta, timezone

query = json.load(sys.stdin)
now = datetime.now(timezone.utc)
try:
    deadline = datetime.fromisoformat(query["termination_time"].replace("Z", "+00:00"))
    valid = (
        deadline.tzinfo is not None
        and now < deadline <= now + timedelta(minutes=30)
    )
except (KeyError, TypeError, ValueError):
    valid = False
print(json.dumps({"valid": str(valid).lower()}))
