INSERT IGNORE INTO seat_definitions
    (event_id, seat_id, section_id, display_alias, row_label, seat_number, allocation_order)
VALUES
    (100, 1, 10, 'R1', 'R', 1, 1),
    (100, 2, 10, 'R2', 'R', 2, 2),
    (100, 3, 10, 'R3', 'R', 3, 3),
    (100, 4, 10, 'R4', 'R', 4, 4),
    (100, 1000, 20, 'S1', 'S', 1, 1),
    (100, 1001, 20, 'S2', 'S', 2, 2),
    (100, 1002, 20, 'S3', 'S', 3, 3),
    (200, 1, 10, 'R1', 'R', 1, 1),
    (200, 2, 10, 'R2', 'R', 2, 2);

INSERT IGNORE INTO seat_inventory
    (event_id, seat_id, section_id, allocation_order)
SELECT event_id, seat_id, section_id, allocation_order
FROM seat_definitions;
