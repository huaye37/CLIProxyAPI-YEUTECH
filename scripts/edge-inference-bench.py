#!/usr/bin/env python3
"""Bounded parallel transport/Responses probes; print timing, never credentials or text."""
import argparse
import concurrent.futures
import json
import http.client
import sys
import time
import urllib.error
import urllib.request

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--url', default='https://llm-api.yeutech.cn')
    parser.add_argument('--model', help='Read API key from stdin and send short Responses requests')
    parser.add_argument('--protocol', choices=['responses','chat','messages'], default='responses')
    parser.add_argument('--connect-ip', help='Force a server IP while preserving TLS hostname verification')
    parser.add_argument('--cancel-on-event', action='store_true', help='Cancel only this synthetic request after its first SSE event')
    parser.add_argument('--count', type=int, default=16)
    parser.add_argument('--workers', type=int, default=8)
    args = parser.parse_args()
    assert 1 <= args.count <= 32 and 1 <= args.workers <= 8
    key = sys.stdin.read().strip() if args.model else None
    class PinnedConnection(http.client.HTTPSConnection):
        def connect(self):
            raw = self._create_connection((args.connect_ip, self.port), self.timeout, self.source_address)
            self.sock = self._context.wrap_socket(raw, server_hostname=self.host)
    class PinnedHandler(urllib.request.HTTPSHandler):
        def https_open(self, request):
            return self.do_open(PinnedConnection, request)
    opener = urllib.request.build_opener(PinnedHandler()) if args.connect_ip else urllib.request.build_opener()
    def probe(index):
        start = time.monotonic()
        result = {'index': index}
        headers = {'Accept-Encoding': 'identity'}
        data = None
        path = '/v1/models'
        if args.model:
            path = '/v1/responses'
            headers.update({'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
            data = json.dumps({'model': args.model, 'input': 'Reply with exactly: OK',
                               'reasoning': {'effort': 'low'}, 'stream': True}).encode()
            if args.protocol in ('chat','messages'):
                path = '/v1/chat/completions' if args.protocol == 'chat' else '/v1/messages'
                headers['anthropic-version'] = '2023-06-01'
                data = json.dumps({'model':args.model,'messages':[{'role':'user','content':'Reply with exactly: OK'}],
                                   'stream':True,'max_tokens':64}).encode()
        try:
            request = urllib.request.Request(args.url.rstrip('/') + path, data=data, headers=headers)
            with opener.open(request, timeout=45 if args.model else 8) as response:
                result['status'] = response.status
                result['headers_s'] = round(time.monotonic() - start, 3)
                if args.model:
                    result['completed'] = False
                    for line in response:
                        if time.monotonic() - start > 60:
                            raise TimeoutError()
                        if not line.startswith(b'data:'):
                            continue
                        if args.protocol == 'chat' and line[5:].strip() == b'[DONE]':
                            result['completed'] = True
                            continue
                        try:
                            event = json.loads(line[5:].strip())
                        except ValueError:
                            continue
                        if args.cancel_on_event:
                            result['canceled_by_probe'] = True
                            break
                        if event.get('type') == 'response.output_text.delta':
                            result.setdefault('first_text_s', round(time.monotonic() - start, 3))
                        if args.protocol == 'chat' and any(c.get('delta',{}).get('content') for c in event.get('choices',[])):
                            result.setdefault('first_text_s', round(time.monotonic() - start, 3))
                        if args.protocol == 'messages' and event.get('delta',{}).get('type') == 'text_delta':
                            result.setdefault('first_text_s', round(time.monotonic() - start, 3))
                        if args.protocol == 'messages' and event.get('type') == 'message_stop':
                            result['completed'] = True
                        if event.get('type') == 'response.completed':
                            result['completed'] = True
                        if event.get('type') in ('error', 'response.failed', 'response.incomplete'):
                            result['event_error'] = event.get('type')
        except urllib.error.HTTPError as error:
            result['status'] = error.code
        except Exception as error:
            result['error'] = type(error).__name__
        result['total_s'] = round(time.monotonic() - start, 3)
        print(json.dumps(result), flush=True)
        return result
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        results = list(pool.map(probe, range(args.count)))
    elapsed = sorted(r['total_s'] for r in results)
    success = sum(r.get('canceled_by_probe', False) if args.cancel_on_event else
                  r.get('completed', False) if args.model else r.get('status') == 401 for r in results)
    print(json.dumps({'summary': {'success': success, 'count': args.count, 'workers': args.workers,
                                 'p50_total_s': elapsed[len(elapsed)//2], 'max_total_s': elapsed[-1]}}))
    return 0 if success == args.count else 1

if __name__ == '__main__':
    sys.exit(main())
