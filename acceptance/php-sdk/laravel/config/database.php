<?php

return [
    'default' => env('DB_CONNECTION', 'pgsql'),
    'connections' => [
        'pgsql' => [
            'driver' => 'pgsql',
            'url' => env('DB_URL'),
            'host' => env('DB_HOST', 'db-must-not-be-used'),
            'port' => env('DB_PORT', '5432'),
            'database' => env('DB_DATABASE', 'control'),
            'username' => env('DB_USERNAME', 'control'),
            'password' => env('DB_PASSWORD', ''),
        ],
    ],
    'migrations' => ['table' => 'migrations', 'update_date_on_publish' => true],
    'redis' => [
        'client' => 'phpredis',
        'default' => [
            'url' => env('REDIS_URL'),
            'host' => env('REDIS_HOST', 'redis-must-not-be-used'),
            'port' => env('REDIS_PORT', '6379'),
        ],
    ],
];
