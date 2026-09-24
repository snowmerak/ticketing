local now_parts = redis.call('TIME')
local now_ms = now_parts[1] * 1000 + math.floor(now_parts[2] / 1000)
local mode = redis.call('HGET', KEYS[1], 'mode')
if not mode then
  return {'RECOVERING', 'control_missing'}
end
if mode == 'RECOVERING' then
  return {'RECOVERING', 'control_recovering'}
end
if mode == 'CLOSED' then
  return {'CLOSED'}
end

local active_epoch = redis.call('GET', KEYS[2])
if active_epoch then
  if mode ~= 'QUEUE' then
    redis.call('HSET', KEYS[1], 'mode', 'RECOVERING')
    return {'RECOVERING', 'mode_epoch_mismatch'}
  end
  if active_epoch ~= ARGV[1] then
    return {'RETRY_EPOCH', active_epoch}
  end
  if redis.call('HGET', KEYS[3], 'state') ~= 'OPEN' then
    redis.call('HSET', KEYS[1], 'mode', 'RECOVERING')
    return {'RECOVERING', 'active_meta_missing'}
  end
  if ARGV[9] ~= '1' then
    return {'KEY_UNAVAILABLE'}
  end
  local seq = redis.call('HINCRBY', KEYS[3], 'next_seq', 1) - 1
  if seq > tonumber(ARGV[8]) then
    return {'CAPACITY_REACHED'}
  end
  local expires_at = now_ms + tonumber(ARGV[5])
  local max_expiry = tonumber(redis.call('HGET', KEYS[3], 'max_ticket_expiry') or '0')
  if expires_at > max_expiry then
    redis.call('HSET', KEYS[3], 'max_ticket_expiry', expires_at)
  end
  return {'TICKET', active_epoch, tostring(seq), tostring(now_ms), tostring(expires_at)}
end

if mode == 'QUEUE' then
  redis.call('HSET', KEYS[1], 'mode', 'RECOVERING')
  return {'RECOVERING', 'active_epoch_missing'}
end

local paused = redis.call('HGET', KEYS[1], 'pause') == '1'
local allow_direct = ARGV[6] == '1'
local capacity = tonumber(redis.call('HGET', KEYS[1], 'capacity') or '0')
local active = tonumber(redis.call('HGET', KEYS[1], 'active_count') or '0')
local reserved = tonumber(redis.call('HGET', KEYS[1], 'reserved_count') or '0')
local rate = tonumber(redis.call('HGET', KEYS[1], 'admission_rate') or '0')
local burst = tonumber(redis.call('HGET', KEYS[1], 'admission_burst') or '0')
local tokens = tonumber(redis.call('HGET', KEYS[1], 'rate_tokens') or tostring(burst))
local token_at = tonumber(redis.call('HGET', KEYS[1], 'rate_token_at_ms') or tostring(now_ms))
if not paused and now_ms > token_at then
  tokens = math.min(burst, tokens + ((now_ms - token_at) / 1000.0) * rate)
  token_at = now_ms
end

if allow_direct and not paused and active + reserved < capacity and tokens >= 1 then
  local absolute_expiry = now_ms + tonumber(ARGV[7])
  local idle_expiry = math.min(now_ms + tonumber(ARGV[4]), absolute_expiry)
  local value = cjson.encode({subject_id=ARGV[2], idle_expires_at_ms=idle_expiry, absolute_expires_at_ms=absolute_expiry})
  redis.call('HSET', KEYS[4], ARGV[3], value)
  redis.call('ZADD', KEYS[5], idle_expiry, ARGV[3])
  redis.call('HINCRBY', KEYS[1], 'active_count', 1)
  redis.call('HSET', KEYS[1], 'rate_tokens', tokens - 1, 'rate_token_at_ms', token_at)
  return {'DIRECT', ARGV[3], tostring(idle_expiry), tostring(absolute_expiry), tostring(now_ms)}
end

if ARGV[9] ~= '1' then
  return {'KEY_UNAVAILABLE'}
end

local expires_at = now_ms + tonumber(ARGV[5])
redis.call('SET', KEYS[2], ARGV[1])
redis.call('HSET', KEYS[3],
  'state', 'OPEN',
  'next_seq', 1,
  'max_ticket_expiry', expires_at,
  'config_version', 1)
redis.call('HSET', KEYS[1], 'mode', 'QUEUE', 'expected_epoch', ARGV[1], 'rate_tokens', tokens, 'rate_token_at_ms', token_at)
return {'TICKET', ARGV[1], '0', tostring(now_ms), tostring(expires_at)}
