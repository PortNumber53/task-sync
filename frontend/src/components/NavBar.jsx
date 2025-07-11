import React from 'react';
import './NavBar.css';

const NAV_LINKS = [
  { label: 'Task Reports', href: '/report' },
];

export default function NavBar() {
  return (
    <nav className="apple-navbar">
        {/* Apple-style logo */}
        <a href="/" className="apple-navbar-logo">
          <span className="text-xl">🍐</span>
        </a>
        
        {/* Center aligned Task Reports link */}
        <div className="apple-navbar-center">
          <a href="/report" className="apple-navbar-link">
            Task Reports
          </a>
        </div>
        
        {/* Right side icons */}
        <div className="apple-navbar-icons">
          <button className="apple-navbar-icon-button">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <circle cx="11" cy="11" r="8"/>
              <path d="M21 21l-4.35-4.35"/>
            </svg>
          </button>
          <button className="apple-navbar-icon-button">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M6 2L3 6v14a2 2 0 002 2h14a2 2 0 002-2V6l-3-4z"/>
              <path d="M3 6h18"/>
              <path d="M16 10a4 4 0 01-8 0"/>
            </svg>
          </button>
        </div>
    </nav>
  );
}
