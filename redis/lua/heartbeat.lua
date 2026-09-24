local now_parts = redis.call('TIME')
local now_ms = now_parts[1] * 1000 + math.floor(now_parts[2] / 1000)
local current_slot = math.floor(now_ms / tonumber(ARGV[7]))
if current_slot ~= tonumber(ARGV[6]) then
  return {'RETRY_SLOT', tostring(current_slot), tostring(now_ms)}
end
if now_ms >= tonumber(ARGV[5]) then
  return {'EXPIRED'}
end
if redis.call('HGET', KEYS[1], 'state') ~= 'OPEN' then
  return {'CLOSED'}
end
if redis.call('GETBIT', KEYS[4], tonumber(ARGV[3])) == 1 then
  return {'SPENT'}
end
redis.call('SETBIT', KEYS[2], tonumber(ARGV[3]), 1)
redis.call('SETBIT', KEYS[3], tonumber(ARGV[4]), 1)
redis.call('PEXPIREAT', KEYS[2], tonumber(ARGV[8]))
redis.call('PEXPIREAT', KEYS[3], tonumber(ARGV[8]))
local max_expiry = tonumber(redis.call('HGET', KEYS[1], 'max_ticket_expiry') or '0')
if tonumber(ARGV[5]) > max_expiry then
  redis.call('HSET', KEYS[1], 'max_ticket_expiry', ARGV[5])
end
local grant_id = redis.call('HGET', KEYS[5], ARGV[1] .. ':' .. ARGV[2])
if grant_id then
  local grant = redis.call('HGET', KEYS[6], grant_id)
  if grant then
    local decoded = cjson.decode(grant)
    if tonumber(decoded.expires_at_ms) > now_ms then
      return {'GRANTED', grant_id, tostring(decoded.expires_at_ms), tostring(now_ms)}
    end
  end
end
return {'WAITING', tostring(now_ms)}
