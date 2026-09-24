CREATE TABLE IF NOT EXISTS schema_migrations (
    version VARCHAR(64) PRIMARY KEY,
    applied_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS seat_definitions (
    event_id BIGINT UNSIGNED NOT NULL,
    seat_id INT UNSIGNED NOT NULL,
    section_id INT UNSIGNED NOT NULL,
    display_alias VARCHAR(64) NOT NULL,
    row_label VARCHAR(32) NULL,
    seat_number INT UNSIGNED NULL,
    allocation_order INT UNSIGNED NOT NULL,
    PRIMARY KEY (event_id, seat_id),
    UNIQUE KEY uq_seat_alias (event_id, section_id, display_alias)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS seat_inventory (
    event_id BIGINT UNSIGNED NOT NULL,
    seat_id INT UNSIGNED NOT NULL,
    section_id INT UNSIGNED NOT NULL,
    allocation_order INT UNSIGNED NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'AVAILABLE',
    hold_id CHAR(32) CHARACTER SET ascii NULL,
    order_id CHAR(32) CHARACTER SET ascii NULL,
    version BIGINT UNSIGNED NOT NULL DEFAULT 0,
    PRIMARY KEY (event_id, seat_id),
    KEY ix_inventory_allocation (event_id, section_id, state, allocation_order, seat_id),
    CONSTRAINT fk_inventory_definition FOREIGN KEY (event_id, seat_id)
        REFERENCES seat_definitions (event_id, seat_id),
    CONSTRAINT ck_inventory_state CHECK (
        (state = 'AVAILABLE' AND hold_id IS NULL AND order_id IS NULL) OR
        (state = 'HELD' AND hold_id IS NOT NULL AND order_id IS NULL) OR
        (state = 'SOLD' AND hold_id IS NULL AND order_id IS NOT NULL)
    )
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS holds (
    hold_id CHAR(32) CHARACTER SET ascii NOT NULL,
    event_id BIGINT UNSIGNED NOT NULL,
    subject_id VARCHAR(128) NOT NULL,
    booking_id CHAR(32) CHARACTER SET ascii NOT NULL,
    state VARCHAR(16) NOT NULL,
    expires_at DATETIME(6) NOT NULL,
    idempotency_key VARCHAR(128) NOT NULL,
    request_hash BINARY(32) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (hold_id),
    UNIQUE KEY uq_hold_idempotency (event_id, subject_id, idempotency_key),
    KEY ix_hold_expiry (state, expires_at, hold_id),
    CONSTRAINT ck_hold_state CHECK (state IN ('HELD', 'CONFIRMED', 'CANCELLED', 'EXPIRED'))
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS hold_items (
    hold_id CHAR(32) CHARACTER SET ascii NOT NULL,
    event_id BIGINT UNSIGNED NOT NULL,
    seat_id INT UNSIGNED NOT NULL,
    PRIMARY KEY (hold_id, seat_id),
    UNIQUE KEY uq_hold_one_seat (hold_id),
    CONSTRAINT fk_hold_item_hold FOREIGN KEY (hold_id) REFERENCES holds (hold_id),
    CONSTRAINT fk_hold_item_seat FOREIGN KEY (event_id, seat_id)
        REFERENCES seat_definitions (event_id, seat_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS orders (
    order_id CHAR(32) CHARACTER SET ascii NOT NULL,
    event_id BIGINT UNSIGNED NOT NULL,
    hold_id CHAR(32) CHARACTER SET ascii NOT NULL,
    subject_id VARCHAR(128) NOT NULL,
    payment_result_id VARCHAR(128) NOT NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (order_id),
    UNIQUE KEY uq_order_hold (hold_id),
    UNIQUE KEY uq_order_subject_event (event_id, subject_id),
    CONSTRAINT fk_order_hold FOREIGN KEY (hold_id) REFERENCES holds (hold_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS order_items (
    order_id CHAR(32) CHARACTER SET ascii NOT NULL,
    event_id BIGINT UNSIGNED NOT NULL,
    seat_id INT UNSIGNED NOT NULL,
    display_alias_snapshot VARCHAR(64) NOT NULL,
    PRIMARY KEY (event_id, seat_id),
    UNIQUE KEY uq_order_one_seat (order_id),
    CONSTRAINT fk_order_item_order FOREIGN KEY (order_id) REFERENCES orders (order_id),
    CONSTRAINT fk_order_item_seat FOREIGN KEY (event_id, seat_id)
        REFERENCES seat_definitions (event_id, seat_id)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS event_purchase_guards (
    event_id BIGINT UNSIGNED NOT NULL,
    subject_id VARCHAR(128) NOT NULL,
    order_id CHAR(32) CHARACTER SET ascii NULL,
    created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (event_id, subject_id),
    UNIQUE KEY uq_purchase_guard_order (order_id),
    CONSTRAINT fk_purchase_guard_order FOREIGN KEY (order_id) REFERENCES orders (order_id)
) ENGINE=InnoDB;
