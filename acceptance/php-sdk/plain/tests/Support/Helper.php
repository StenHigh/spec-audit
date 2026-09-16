<?php

declare(strict_types=1);

namespace Tests\Support;

use Demo\FileStore;

final class Helper
{
    public static function makeStore(): FileStore
    {
        return new FileStore();
    }
}
