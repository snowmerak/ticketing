local now_parts = redis.call('TIME')
local now_ms = now_parts[1] * 1000 + math.floor(now_parts[2] / 1000)
if redis.call('GET', KEYS[2]) ~= ARGV[1] then return {'NOT_ACTIVE'} end
if redis.call('HGET', KEYS[3], 'state') ~= 'OPEN' then return {'CLOSED'} end
if tonumber(redis.call('HGET', KEYS[3], 'max_ticket_expiry') or '0') > now_ms then return {'TICKETS_VALID'} end
if redis.call('HLEN', KEYS[4]) > 0 then return {'GRANTS_PENDING'} end
redis.call('HSET', KEYS[3], 'state', 'CLOSED')
redis.call('DEL', KEYS[2])
redis.call('HSET', KEYS[1], 'mode', 'DIRECT', 'expected_epoch', '')
return {'CLOSED'}
