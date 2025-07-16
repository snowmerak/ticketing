#!/usr/bin/env python3
"""
Ticketing API Test Script
"""

import json
import requests
import time
from datetime import datetime, timedelta
import uuid

BASE_URL = "http://localhost:8080/api/v1"

def test_health():
    """Test health endpoint"""
    print("=== Health Check ===")
    response = requests.get("http://localhost:8080/health")
    print(f"Status: {response.status_code}")
    print(f"Response: {response.text}")
    return response.status_code == 200

def create_event():
    """Create a test event"""
    print("\n=== Creating Event ===")
    
    now = datetime.now()
    start_time = now + timedelta(hours=1)
    end_time = now + timedelta(hours=5)
    
    event_data = {
        "name": "Test Concert",
        "description": "A test concert event",
        "start_time": start_time.isoformat() + "Z",
        "end_time": end_time.isoformat() + "Z",
        "venue": "Test Venue",
        "total_tickets": 100,
        "is_seated_event": True
    }
    
    print(f"Request data: {json.dumps(event_data, indent=2)}")
    
    response = requests.post(
        f"{BASE_URL}/events",
        json=event_data,
        headers={"Content-Type": "application/json"}
    )
    
    print(f"Status: {response.status_code}")
    print(f"Response: {response.text}")
    
    if response.status_code == 201:
        return response.json()["id"]
    return None

def get_event(event_id):
    """Get event details"""
    print(f"\n=== Getting Event {event_id} ===")
    
    response = requests.get(f"{BASE_URL}/events/{event_id}")
    print(f"Status: {response.status_code}")
    print(f"Response: {json.dumps(response.json(), indent=2)}")
    
    return response.status_code == 200

def create_seats(event_id):
    """Create seats for an event"""
    print(f"\n=== Creating Seats for Event {event_id} ===")
    
    seats_data = {
        "seats": [
            {"section": "A", "row": "1", "number": "1", "price": 5000},
            {"section": "A", "row": "1", "number": "2", "price": 5000},
            {"section": "A", "row": "1", "number": "3", "price": 5000},
            {"section": "A", "row": "2", "number": "1", "price": 5000},
            {"section": "A", "row": "2", "number": "2", "price": 5000},
            {"section": "A", "row": "2", "number": "3", "price": 5000},
            {"section": "B", "row": "1", "number": "1", "price": 4000},
            {"section": "B", "row": "1", "number": "2", "price": 4000}
        ]
    }
    
    print(f"Request data: {json.dumps(seats_data, indent=2)}")
    
    response = requests.post(
        f"{BASE_URL}/events/{event_id}/seats",
        json=seats_data,
        headers={"Content-Type": "application/json"}
    )
    
    print(f"Status: {response.status_code}")
    print(f"Response: {response.text}")
    
    return response.status_code == 201

def join_queue(event_id, user_id):
    """Join queue for an event"""
    print(f"\n=== Joining Queue for Event {event_id} with User {user_id} ===")
    
    queue_data = {
        "event_id": event_id,
        "user_id": user_id,
        "session_id": f"session_{user_id}_{int(time.time())}"
    }
    
    print(f"Request data: {json.dumps(queue_data, indent=2)}")
    
    response = requests.post(
        f"{BASE_URL}/queue/join",
        json=queue_data,
        headers={"Content-Type": "application/json"}
    )
    
    print(f"Status: {response.status_code}")
    print(f"Response: {response.text}")
    
    if response.status_code == 200:
        return response.json()["session_id"]
    return None

def get_queue_position(event_id, user_id):
    """Get queue position"""
    print(f"\n=== Getting Queue Position for Event {event_id} User {user_id} ===")
    
    response = requests.get(f"{BASE_URL}/queue/position/{event_id}/{user_id}")
    print(f"Status: {response.status_code}")
    print(f"Response: {json.dumps(response.json(), indent=2)}")
    
    return response.status_code == 200

def purchase_ticket(event_id, user_id, session_id, seat_id=None):
    """Purchase a ticket"""
    print(f"\n=== Purchasing Ticket for Event {event_id} User {user_id} ===")
    
    ticket_data = {
        "event_id": event_id,
        "user_id": user_id,
        "session_id": session_id
    }
    
    if seat_id:
        ticket_data["seat_id"] = seat_id
    
    print(f"Request data: {json.dumps(ticket_data, indent=2)}")
    
    response = requests.post(
        f"{BASE_URL}/tickets/purchase",
        json=ticket_data,
        headers={"Content-Type": "application/json"}
    )
    
    print(f"Status: {response.status_code}")
    print(f"Response: {response.text}")
    
    if response.status_code == 201:
        return response.json()["id"]
    return None

def get_available_seats(event_id):
    """Get available seats for an event"""
    print(f"\n=== Getting Available Seats for Event {event_id} ===")
    
    response = requests.get(f"{BASE_URL}/events/{event_id}/seats/available")
    print(f"Status: {response.status_code}")
    
    if response.status_code == 200:
        seats = response.json()
        print(f"Available seats: {len(seats)}")
        for seat in seats[:5]:  # Show first 5 seats
            print(f"  - {seat['section']}-{seat['row']}-{seat['number']}: {seat['price']} (ID: {seat['id']})")
        if len(seats) > 5:
            print(f"  ... and {len(seats) - 5} more seats")
        return seats
    else:
        print(f"Response: {response.text}")
    return []

def main():
    """Main test function"""
    print("Starting Ticketing API Tests...")
    
    # Test health
    if not test_health():
        print("Health check failed!")
        return
    
    # Create event
    event_id = create_event()
    if not event_id:
        print("Failed to create event!")
        return
    
    print(f"Created event with ID: {event_id}")
    
    # Get event details
    get_event(event_id)
    
    # Create seats
    if not create_seats(event_id):
        print("Failed to create seats!")
        return
    
    # Get available seats
    seats = get_available_seats(event_id)
    
    # Generate test users
    user1_id = str(uuid.uuid4())
    user2_id = str(uuid.uuid4())
    
    print(f"Test users: {user1_id}, {user2_id}")
    
    # Join queue
    session1 = join_queue(event_id, user1_id)
    session2 = join_queue(event_id, user2_id)
    
    # Get queue positions
    get_queue_position(event_id, user1_id)
    get_queue_position(event_id, user2_id)
    
    # Purchase ticket (if we have seats)
    if seats and session1:
        seat_id = seats[0]["id"]
        ticket_id = purchase_ticket(event_id, user1_id, session1, seat_id)
        if ticket_id:
            print(f"Purchased ticket with ID: {ticket_id}")
    
    # Get updated available seats
    get_available_seats(event_id)
    
    print("\nAPI Tests completed!")

if __name__ == "__main__":
    main()
