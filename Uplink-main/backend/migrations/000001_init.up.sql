CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE users (
                       id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                       username VARCHAR(32) UNIQUE NOT NULL,
                       password_hash VARCHAR(255) NOT NULL,
                       rating INT DEFAULT 1000
);

CREATE TABLE texts (
                       id SERIAL PRIMARY KEY,
                       content TEXT NOT NULL,
                       language VARCHAR(5) DEFAULT 'en',
                       category VARCHAR(20) DEFAULT 'general',
                       length INT NOT NULL
);

CREATE TABLE words (
                       id SERIAL PRIMARY KEY,
                       content VARCHAR(50) NOT NULL,
                       language VARCHAR(5) DEFAULT 'en'
);

CREATE TABLE matches (
                         id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
                         text_id INT REFERENCES texts(id),
                         ended_at TIMESTAMP WITH TIME ZONE
);

CREATE TABLE match_results (
                               match_id UUID REFERENCES matches(id),
                               user_id UUID REFERENCES users(id),
                               wpm INT NOT NULL,
                               accuracy DECIMAL(5,2) NOT NULL,
                               rank INT NOT NULL,
                               PRIMARY KEY (match_id, user_id)
);

CREATE INDEX idx_match_results_user_id ON match_results(user_id);
CREATE INDEX idx_matches_ended_at ON matches(ended_at);