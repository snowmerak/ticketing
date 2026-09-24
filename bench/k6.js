import http from 'k6/http';
import { check, fail } from 'k6';
import { Trend } from 'k6/metrics';
import exec from 'k6/execution';

const profile = __ENV.PROFILE;
const runID = __ENV.RUN_ID || 'single';
const eventID = __ENV.EVENT_ID;
const baseURL = __ENV.BASE_URL;
const capacity = Number(__ENV.CAPACITY || 64);
const seatBase = Number(__ENV.SEAT_BASE || 1);
const profiles = ['entry', 'queue', 'seat-map', 'hold-cancel', 'confirm', 'full'];
if (!profiles.includes(profile) || !eventID || !baseURL) {
  throw new Error('PROFILE, EVENT_ID and BASE_URL are required');
}

export const options = {
  scenarios: {
    bench: {
      executor: 'shared-iterations',
      vus: Number(__ENV.VUS || 4),
      iterations: Number(__ENV.ITERATIONS || 40),
      maxDuration: '30s',
    },
  },
  thresholds: {
    checks: ['rate==1'],
    http_req_failed: ['rate==0'],
  },
};

const entryMs = new Trend('entry_ms');
const queueEntryMs = new Trend('queue_entry_ms');
const queueHeartbeatMs = new Trend('queue_heartbeat_ms');
const seatMapMs = new Trend('seat_map_ms');
const holdMs = new Trend('hold_ms');
const cancelMs = new Trend('cancel_ms');
const confirmMs = new Trend('confirm_ms');
const journeyMs = new Trend('journey_ms');

function post(path, subject, body, expected, metric, extraHeaders = {}) {
  const step = path.replace(/[0-9a-f]{32}/g, ':hold').replace(/\d+/g, ':id');
  const response = http.post(`${baseURL}${path}`, JSON.stringify(body), {
    headers: { 'Content-Type': 'application/json', 'X-Subject-ID': subject, ...extraHeaders },
    tags: { step },
  });
  if (metric) metric.add(response.timings.duration);
  const ok = check(response, { [`${step} status ${expected}`]: (r) => r.status === expected });
  if (!ok) fail(`${path}: HTTP ${response.status} ${response.body}`);
  return response.json();
}

function entry(subject, metric = entryMs) {
  const result = post(`/events/${eventID}/entry`, subject, {}, 200, metric);
  return result;
}

function leave(subject, bookingID) {
  post('/booking/leave', subject, { event_id: eventID, booking_id: bookingID }, 200);
}

function seatMap(subject, bookingID) {
  const response = http.get(`${baseURL}/events/${eventID}/seat-map`, {
    headers: { 'X-Subject-ID': subject, 'X-Booking-ID': bookingID },
    tags: { step: 'seat-map' },
  });
  seatMapMs.add(response.timings.duration);
  const ok = check(response, { 'seat-map status 200 and 256 seats': (r) => r.status === 200 && r.json('seats').length === 256 });
  if (!ok) fail(`seat-map: HTTP ${response.status} ${response.body}`);
}

function hold(subject, bookingID, seatID, iteration) {
  return post(`/events/${eventID}/holds`, subject, {
    booking_id: bookingID, mode: 'specified', seat_ids: [String(seatID)], quantity: 1,
  }, 201, holdMs, { 'Idempotency-Key': `${runID}-${profile}-${seatBase}-${iteration}` });
}

export function setup() {
  if (profile !== 'queue') return null;
  const occupiers = [];
  for (let i = 0; i < capacity; i++) {
    const subject = `bench-occupier-${i}`;
    const result = entry(subject, null);
    if (result.kind !== 'DIRECT') fail(`queue setup admission ${i}: ${JSON.stringify(result)}`);
    occupiers.push([subject, result.booking.booking_id]);
  }
  return { occupiers };
}

export default function () {
  const iteration = exec.scenario.iterationInTest;
  const subject = `bench-${runID}-${profile}-${seatBase}-${iteration}`;
  const started = Date.now();
  const admitted = entry(subject, profile === 'queue' ? queueEntryMs : entryMs);

  if (profile === 'queue') {
    if (admitted.kind !== 'QUEUE') fail(`expected QUEUE, got ${JSON.stringify(admitted)}`);
    const heartbeat = post('/queue/heartbeat', subject,
      { event_id: eventID, ticket: admitted.ticket }, 200, queueHeartbeatMs);
    if (heartbeat.status !== 'WAITING') fail(`expected WAITING, got ${JSON.stringify(heartbeat)}`);
  } else {
    if (admitted.kind !== 'DIRECT') fail(`expected DIRECT, got ${JSON.stringify(admitted)}`);
    const bookingID = admitted.booking.booking_id;
    if (profile === 'seat-map' || profile === 'full') seatMap(subject, bookingID);
    if (profile === 'hold-cancel' || profile === 'confirm' || profile === 'full') {
      const seatID = profile === 'hold-cancel' ? exec.vu.idInTest : seatBase + iteration;
      const reserved = hold(subject, bookingID, seatID, iteration);
      if (profile === 'hold-cancel') {
        const cancelled = post(`/holds/${reserved.hold_id}/cancel`, subject, {}, 200, cancelMs);
        if (cancelled.state !== 'CANCELLED') fail(`cancelled hold state: ${cancelled.state}`);
      } else {
        const order = post(`/holds/${reserved.hold_id}/confirm`, subject,
          { booking_id: bookingID, payment_result_id: `bench-payment-${runID}-${profile}-${seatBase}-${iteration}` },
          200, confirmMs);
        if (!order.order_id) fail('confirm response has no order_id');
      }
    }
    leave(subject, bookingID);
  }
  journeyMs.add(Date.now() - started);
}

export function teardown(data) {
  if (!data || !data.occupiers) return;
  for (const [subject, bookingID] of data.occupiers) leave(subject, bookingID);
}
