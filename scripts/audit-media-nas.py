#!/usr/bin/env python3
"""Run on NAS through existing SSH, using a client key supplied on stdin.

Print only synthetic generation metadata, never credentials or raw errors.
"""
import base64
import json
import sys
import time
import urllib.error
import urllib.request

key = sys.stdin.readline().strip()
entries = json.loads(sys.stdin.readline())
for entry in entries:
    started = time.monotonic()
    result = {'id': entry['model'], 'path': entry['path'], 'scope': 'nas-direct'}
    payload = entry['body']
    request = urllib.request.Request('http://127.0.0.1:18319' + entry['path'],
        data=json.dumps(payload).encode(), headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
    try:
        with urllib.request.urlopen(request, timeout=90) as response:
            raw = response.read(24 * 1024 * 1024)
            result.update(httpStatus=response.status, contentType=response.headers.get('Content-Type'))
        try:
            data = json.loads(raw)
        except ValueError:
            data = {}
        if '/audio/' in entry['path']:
            result.update(status='passed_audio_response' if result['contentType'].startswith('audio/') and len(raw)>64 else 'unexpected_response', bytes=len(raw))
        elif '/images/' in entry['path']:
            sizes=[]
            for item in data.get('data', []):
                binary=base64.b64decode(item.get('b64_json',''))
                if len(binary)>64 and (binary.startswith(b'\x89PNG\r\n\x1a\n') or binary.startswith(b'\xff\xd8') or binary.startswith(b'RIFF')):
                    sizes.append(len(binary))
            result.update(status='passed_image_bytes' if sizes else 'image_url_returned_unverified' if any(item.get('url') for item in data.get('data',[])) else 'unexpected_response', imageBytes=sizes)
        else:
            result.update(status='video_job_accepted_not_completed', requestId=data.get('request_id') or data.get('id'))
    except urllib.error.HTTPError as error:
        raw=error.read(8192).decode(errors='replace').lower()
        reason='request_rejected' if error.code==400 else 'endpoint_not_found' if error.code==404 else 'upstream_error'
        if 'credits' in raw and ('required' in raw or 'insufficient' in raw): reason='credits_required'
        elif 'not supported' in raw: reason='not_supported'
        elif 'quota' in raw: reason='quota_or_limit'
        elif error.code==429: reason='rate_limited'
        elif error.code in (401,403): reason='authorization_failed'
        result.update(httpStatus=error.code,status=reason)
    except Exception as error:
        result.update(status='transport_error',errorType=type(error).__name__)
    result['seconds']=round(time.monotonic()-started,3)
    print(json.dumps(result),flush=True)
