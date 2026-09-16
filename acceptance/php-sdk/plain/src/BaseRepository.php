<?php

declare(strict_types=1);

namespace Demo;

abstract class BaseRepository
{
    public function find(int $id): ?User
    {
        return $id > 0 ? new User('user-' . $id) : null;
    }
}
